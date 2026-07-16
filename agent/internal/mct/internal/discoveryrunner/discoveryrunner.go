package discoveryrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	integration "github.com/tursomari/machtiani/agent/internal/file-discovery/integration"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

// Result holds parsed file paths from file-discovery output
type Result struct {
	Paths []string
}

type ModelSettings struct {
	UsingAlias         bool
	Alias              string
	Resolved           llm.ResolvedModel
	Extras             map[string]any
	ParamPairs         []string
	ParamJSON          []string
	FallbackAliases    []string
	FallbackResolved   []llm.ResolvedModel
	TrajectoryOverride string
	APIKeyOverrides    map[string]string
	TurnTimeout        int
	InputBudget        llm.InputBudget
	PromptTruncated    bool
}

const (
	emergencyInputFloorBytes = 1 << 20
	emergencyInputMaxBytes   = 64 << 20
	emergencyBytesPerToken   = 16
)

// EmergencyInitialInputBytes is a secondary memory-safety ceiling. Token
// fitting remains the product limit; this only rejects unexpectedly large
// embedded inputs before they are copied into the discovery history.
func EmergencyInitialInputBytes(maxInputTokens int) int {
	derived := maxInputTokens * emergencyBytesPerToken
	if derived < emergencyInputFloorBytes {
		return emergencyInputFloorBytes
	}
	if derived > emergencyInputMaxBytes {
		return emergencyInputMaxBytes
	}
	return derived
}

func InitialPromptTokenLimit(budget llm.InputBudget) (int, error) {
	fixed := integration.FixedInputTokens(integration.ToolCallModeJSON)
	limit := budget.MaxInputTokens - fixed
	if limit <= 0 {
		return 0, fmt.Errorf("discovery context budget %d cannot fit fixed system/protocol content (%d estimated tokens)", budget.MaxInputTokens, fixed)
	}
	return limit, nil
}

func FitInitialPrompt(prompt string, budget llm.InputBudget) (string, bool, error) {
	limit, err := InitialPromptTokenLimit(budget)
	if err != nil {
		return "", false, err
	}
	fitted, truncated, err := llm.TruncateLinesKeepTail(prompt, limit, "[TRUNCATED: older discovery cue material omitted]")
	if err != nil {
		return "", truncated, err
	}
	if err := llm.RequireWithinTokenBudget("discovery initial prompt", fitted, limit); err != nil {
		return "", truncated, err
	}
	return fitted, truncated, nil
}

// Matches blocks like:
// BEGIN_RELEVANT_FILES[session]
// <paths...>
// END_RELEVANT_FILES[session]
// Go's regexp doesn't support backreferences, so capture both session ids and compare in code.
var blockRe = regexp.MustCompile(`(?s)BEGIN_RELEVANT_FILES\[(.+?)\]\n(.*?)\nEND_RELEVANT_FILES\[(.+?)\]`)

const exitCodeNoRelevantFiles = 2

// isSafePath performs defense-in-depth checks on a relative file path
func isSafePath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	if strings.Contains(p, "\\") { // no backslashes
		return false
	}
	if strings.Contains(p, "..") { // disallow parent traversal
		return false
	}
	if strings.HasSuffix(p, "/") { // no trailing slash directories
		return false
	}
	clean := filepath.Clean(p)
	if strings.HasPrefix(clean, "..") { // extra guard after Clean
		return false
	}
	return true
}

// uniqOrder deduplicates while preserving order
func uniqOrder(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Run executes file-discovery with the given prompt and environment mapping.
// It returns the list of discovered relative paths.
func Run(ctx context.Context, prompt string, model ModelSettings, sessionID string, verbose bool) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	effectiveSessionID := strings.TrimSpace(sessionID)
	if effectiveSessionID == "" {
		effectiveSessionID = uuid.New().String()
	}

	extraParams, err := llm.ParseParamOverrides(model.ParamPairs, model.ParamJSON)
	if err != nil {
		return Result{}, fmt.Errorf("parse llm parameter overrides: %w", err)
	}
	mergedExtras := mergeExtras(model.Extras, extraParams)
	initialLimit, err := InitialPromptTokenLimit(model.InputBudget)
	if err != nil {
		return Result{}, err
	}
	if err := llm.RequireWithinTokenBudget("discovery initial prompt", prompt, initialLimit); err != nil {
		return Result{}, err
	}

	resolved := llm.CloneResolvedModel(model.Resolved)
	cfg := integration.Config{
		MaxRounds:            20,
		CmdTimeoutSec:        30,
		LLMTimeoutSec:        model.TurnTimeout,
		MaxStdoutBytes:       20480,
		MaxInitialInputBytes: EmergencyInitialInputBytes(model.InputBudget.MaxInputTokens),
		Verbose:              verbose,
		SessionID:            effectiveSessionID,
		ToolCallMode:         integration.ToolCallModeJSON,
		APIKey:               strings.TrimSpace(resolved.APIKey),
		BaseURL:              strings.TrimSpace(resolved.BaseURL),
		Model:                strings.TrimSpace(resolved.Model),
	}
	if cfg.APIKey == "" || cfg.BaseURL == "" || cfg.Model == "" {
		return Result{}, errors.New("file-discovery runtime missing API key, base URL, or model")
	}
	llmSettings := integration.LLMSettings{
		Model:                      resolved,
		Extras:                     mergedExtras,
		FallbackAliases:            append([]string(nil), model.FallbackAliases...),
		FallbackResolved:           cloneResolvedModels(model.FallbackResolved),
		APIKeyOverrides:            llm.CopyAPIKeyOverridesForRuntime(model.APIKeyOverrides),
		InputBudget:                model.InputBudget,
		ContextIdentityUnambiguous: model.UsingAlias && strings.TrimSpace(resolved.Alias) != "" && len(model.FallbackAliases) == 0 && len(model.FallbackResolved) == 0,
	}
	debugf(verbose, "mct: using embedded file-discovery module")

	if override := strings.TrimSpace(model.TrajectoryOverride); override != "" {
		if err := os.MkdirAll(filepath.Dir(override), 0o755); err != nil {
			return Result{}, fmt.Errorf("create file-discovery trajectory directory: %w", err)
		}
		cfg.TrajectoryPath = override
	} else if effectiveSessionID != "" {
		trajectoryPath, err := artifacts.FileDiscoveryTrajectoryPath(effectiveSessionID)
		if err != nil {
			return Result{}, fmt.Errorf("resolve file-discovery trajectory path: %w", err)
		}
		if err := os.MkdirAll(filepath.Dir(trajectoryPath), 0o755); err != nil {
			return Result{}, fmt.Errorf("create file-discovery trajectory directory: %w", err)
		}
		cfg.TrajectoryPath = trajectoryPath
	}

	workspaceRoot, err := os.Getwd()
	if err != nil {
		return Result{}, fmt.Errorf("discovery workspace getwd: %w", err)
	}
	snapshotRoot := strings.TrimSpace(os.Getenv("MACHTIANI_TMP_ROOT"))
	if snapshotRoot != "" {
		snapshotPath := filepath.Join(snapshotRoot, "repo")
		st, err := os.Stat(snapshotPath)
		if err != nil {
			// When running file discovery under mct-agent, the session runner should
			// have created a snapshot at $MACHTIANI_TMP_ROOT/repo. If it did not (e.g.
			// earlier snapshot preparation failed), falling back to the current working
			// directory keeps file discovery usable and lets the user see the original
			// error elsewhere.
			debugf(verbose, "mct: discovery snapshot missing at %s (%v); falling back to current working directory", snapshotPath, err)
		} else if !st.IsDir() {
			debugf(verbose, "mct: discovery snapshot path is not a directory at %s; falling back to current working directory", snapshotPath)
		} else {
			workspaceRoot = snapshotPath
			debugf(verbose, "mct: discovery snapshot enabled at %s", snapshotPath)
		}
	} else {
		// No snapshot configured; operate on current working directory (e.g., sync command)
		debugf(verbose, "mct: MACHTIANI_TMP_ROOT not set; file-discovery will use current working directory")
	}
	cfg.WorkspaceRoot = workspaceRoot

	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	default:
	}

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return Result{}, fmt.Errorf("pipe stdout: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return Result{}, fmt.Errorf("pipe stderr: %w", err)
	}
	originalStdout := os.Stdout
	originalStderr := os.Stderr
	os.Stdout = stdoutW
	os.Stderr = stderrW

	originalLogWriter := log.Writer()
	originalLogFlags := log.Flags()
	originalLogPrefix := log.Prefix()
	log.SetOutput(stderrW)
	log.SetFlags(0)
	log.SetPrefix("")

	defer func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
		log.SetOutput(originalLogWriter)
		log.SetFlags(originalLogFlags)
		log.SetPrefix(originalLogPrefix)
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
	}()

	var stdoutBuf, stderrBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stdoutBuf, stdoutR)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&stderrBuf, stderrR)
	}()

	exitCode := integration.RunEmbedded(ctx, cfg, llmSettings, prompt)

	_ = stdoutW.Close()
	_ = stderrW.Close()
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	if exitCode == exitCodeNoRelevantFiles {
		return Result{}, nil
	}
	if exitCode != 0 {
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg != "" {
			return Result{}, fmt.Errorf("file-discovery failed: exit code %d\n%s", exitCode, errMsg)
		}
		return Result{}, fmt.Errorf("file-discovery failed: exit code %d", exitCode)
	}

	outStr := stdoutBuf.String()
	m := blockRe.FindStringSubmatch(outStr)
	if len(m) != 4 {
		return Result{}, errors.New("failed to parse relevant files block from file-discovery output")
	}
	if m[1] != m[3] {
		return Result{}, errors.New("mismatched session ids in relevant files block")
	}

	block := m[2]
	lines := strings.Split(block, "\n")
	var paths []string
	for _, line := range lines {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if !isSafePath(p) {
			continue
		}
		paths = append(paths, p)
	}

	paths = uniqOrder(paths)
	return Result{Paths: paths}, nil
}

// useGitFilter reads MCT_USE_GIT_FILTER and returns true unless explicitly disabled.
// debugf writes debug lines to stderr when MCT_DEBUG is set.
func debugf(verbose bool, format string, args ...any) {
	if !verbose {
		return
	}
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

func normalizeRepoPath(in string) string {
	trimmed := strings.TrimSpace(in)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return ""
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, `..`+string(os.PathSeparator)) {
		return ""
	}
	normalized := filepath.ToSlash(cleaned)
	normalized = strings.ReplaceAll(normalized, "\\", "/")
	return normalized
}

func drainDiscoveryScratch(sessionID string, verbose bool) []string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	scratch, err := artifacts.SessionScratchDirectory(sessionID)
	if err != nil {
		debugf(verbose, "mct: unable to resolve discovery scratch directory: %v", err)
		return nil
	}
	path := filepath.Join(scratch, "file-discovery", "pending.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			debugf(verbose, "mct: unable to read discovery pending file: %v", err)
		}
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		debugf(verbose, "mct: unable to clear discovery pending file: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	set := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		norm := normalizeRepoPath(line)
		if norm != "" {
			set[norm] = struct{}{}
		}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for rel := range set {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func mergeExtras(base map[string]any, overrides map[string]any) map[string]any {
	if len(base) == 0 && len(overrides) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}

func cloneResolvedModels(src []llm.ResolvedModel) []llm.ResolvedModel {
	if len(src) == 0 {
		return nil
	}
	out := make([]llm.ResolvedModel, 0, len(src))
	for _, m := range src {
		out = append(out, llm.CloneResolvedModel(m))
	}
	return out
}
