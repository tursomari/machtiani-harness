package prompt
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/contextbuilder"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

const (
	maxShowFilePaths     = 10
	showFileSystemPrompt = "You detect whether a planner prompt is asking to show or display the full content of one or more specific repository files. Reply with ONLY valid JSON. If it is a request to show full file contents, reply as: {\"is_show_file_request\":true,\"filepaths\":[\"path/to/file\",\"another/file\"],\"reason\":\"short reason for the request\"}. The filepaths must be workspace-relative paths, with no more than 10 files. For a single file you may also include the legacy \"filepath\" field set to the same path. If not a show-file request, reply as: {\"is_show_file_request\":false}."
	showFileQuestionTmpl = "Planner prompt:\n\n%s\n\nReturn the JSON now."
)

type ShowFileDetection struct {
	IsShowFileRequest bool     `json:"is_show_file_request"`
	Filepath          string   `json:"filepath"`
	Filepaths         []string `json:"filepaths"`
	Reason            string   `json:"reason,omitempty"`
	VerbatimQuestion  string   `json:"-"`
}

func (s ShowFileDetection) normalize() ShowFileDetection {
	if !s.IsShowFileRequest {
		s.Filepath = ""
		s.Filepaths = nil
		s.Reason = ""
		return s
	}
	paths := append([]string(nil), s.Filepaths...)
	if strings.TrimSpace(s.Filepath) != "" {
		paths = append(paths, s.Filepath)
	}
	s.Filepaths = normalizeShowFilePaths(paths)
	if len(s.Filepaths) == 0 {
		s.IsShowFileRequest = false
		s.Filepath = ""
		s.Reason = ""
		return s
	}
	s.Filepath = s.Filepaths[0]
	s.Reason = strings.TrimSpace(s.Reason)
	return s
}

// DetectShowFileRequest runs an LLM classification pass to determine whether
// the planner is requesting that we show the full contents of a particular
// repository file.
func DetectShowFileRequest(ctx context.Context, runtime ModelRuntime, plannerPrompt string) (ShowFileDetection, string, error) {
	verbatimPrompt := plannerPrompt
	trimmedPrompt := strings.TrimSpace(plannerPrompt)
	if trimmedPrompt == "" {
		return ShowFileDetection{IsShowFileRequest: false}, "", nil
	}

	messages := []llm.Message{
		{Role: "system", Content: showFileSystemPrompt},
		{Role: "user", Content: fmt.Sprintf(showFileQuestionTmpl, trimmedPrompt)},
	}

	resolved := llm.CloneResolvedModel(runtime.Resolved)
	fallbackAliases := append([]string(nil), runtime.FallbackAliases...)
	fallbackResolved := cloneResolvedModels(runtime.FallbackResolved)
	extras := copyExtrasMap(runtime.Extras)

	resp, err := chatWithResolvedFallback(ctx, resolved, fallbackAliases, fallbackResolved, extras, messages)
	if err != nil {
		return ShowFileDetection{IsShowFileRequest: false}, "", err
	}

	raw := strings.TrimSpace(resp)
	if raw == "" {
		return ShowFileDetection{IsShowFileRequest: false}, "", nil
	}
	// Models sometimes wrap JSON in markdown code fences or include extra text.
	// Try to extract the first JSON object from the response.
	if strings.HasPrefix(raw, "```") {
		trimmed := strings.TrimSpace(raw)
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```JSON")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		raw = strings.TrimSpace(trimmed)
	}
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}

	var det ShowFileDetection
	if jerr := json.Unmarshal([]byte(raw), &det); jerr != nil {
		return ShowFileDetection{IsShowFileRequest: false}, raw, jerr
	}
	det.VerbatimQuestion = verbatimPrompt
	return det.normalize(), raw, nil
}

type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type SnippetDiscoveryPartialError struct {
	Missing []string
	Invalid map[string]string
}

func (e *SnippetDiscoveryPartialError) Error() string {
	if e == nil {
		return "snippet-discovery returned partial results"
	}
	parts := []string{}
	if len(e.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing files: %s", strings.Join(e.Missing, ", ")))
	}
	if len(e.Invalid) > 0 {
		paths := make([]string, 0, len(e.Invalid))
		for path := range e.Invalid {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		details := make([]string, 0, len(paths))
		for _, path := range paths {
			details = append(details, fmt.Sprintf("%s (%s)", path, e.Invalid[path]))
		}
		parts = append(parts, fmt.Sprintf("invalid snippets: %s", strings.Join(details, ", ")))
	}
	if len(parts) == 0 {
		return "snippet-discovery returned partial results"
	}
	return strings.Join(parts, "; ")
}

var (
	snippetDiscoveryCommandContext = exec.CommandContext
	runSnippetDiscovery            = invokeSnippetDiscovery
)

// FetchFileSnippets runs snippet-discovery with a model alias (typically the file-discovery model).
func FetchFileSnippets(ctx context.Context, detection ShowFileDetection, repoRoot, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (map[string][]LineRange, error) {
	filepaths := normalizeShowFilePaths(detection.Filepaths)
	if len(filepaths) == 0 {
		return nil, errors.New("no file paths provided")
	}
	reason := detection.VerbatimQuestion
	if strings.TrimSpace(reason) == "" {
		reason = normalizeSnippetReason(detection.Reason, filepaths)
	}
	raw, err := runSnippetDiscovery(ctx, repoRoot, reason, filepaths, modelAlias, apiKeyOverrides, verbose)
	if err != nil {
		return nil, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("snippet-discovery returned empty output")
	}
	var decoded map[string][]LineRange
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("parse snippet-discovery output: %w", err)
	}
	cleaned := make(map[string][]LineRange, len(decoded))
	invalid := map[string]string{}
	for path, ranges := range decoded {
		normalized := normalizeShowFilePath(path)
		if normalized == "" {
			invalid[path] = "invalid path"
			continue
		}
		if len(ranges) == 0 {
			cleaned[normalized] = []LineRange{}
			continue
		}
		validRanges := make([]LineRange, 0, len(ranges))
		for _, r := range ranges {
			if r.Start <= 0 || r.End <= 0 || r.End < r.Start {
				invalid[normalized] = fmt.Sprintf("invalid range %d-%d", r.Start, r.End)
				continue
			}
			validRanges = append(validRanges, r)
		}
		if len(validRanges) > 0 {
			cleaned[normalized] = validRanges
		} else {
			cleaned[normalized] = []LineRange{}
		}
	}
	missing := missingSnippetFiles(filepaths, cleaned)
	var partialErr *SnippetDiscoveryPartialError
	if len(missing) > 0 || len(invalid) > 0 {
		partialErr = &SnippetDiscoveryPartialError{Missing: missing, Invalid: invalid}
	}
	if partialErr != nil && len(partialErr.Missing) == 0 && len(partialErr.Invalid) == 0 {
		partialErr = nil
	}
	if len(cleaned) == 0 {
		if partialErr != nil {
			return nil, partialErr
		}
		return nil, errors.New("snippet-discovery returned no snippets")
	}
	if partialErr != nil {
		return cleaned, partialErr
	}
	return cleaned, nil
}

func formatSnippetsResponse(snippets map[string][]LineRange, reason string) string {
	formatted, _, _ := formatSnippetsResponseWithRoot(snippets, reason, "")
	return formatted
}

func FormatSnippetsResponse(snippets map[string][]LineRange, reason, repoRoot string) (string, []string, []string) {
	return formatSnippetsResponseWithRoot(snippets, reason, repoRoot)
}

func formatFullFileFallback(filepaths []string, err error) string {
	formatted, _, _ := formatFullFileFallbackWithRoot(filepaths, "", err)
	return formatted
}

func FormatFullFileFallback(filepaths []string, repoRoot string, err error) (string, []string, []string) {
	return formatFullFileFallbackWithRoot(filepaths, repoRoot, err)
}

func formatSnippetsResponseWithRoot(snippets map[string][]LineRange, reason, repoRoot string) (string, []string, []string) {
	var b strings.Builder
	paths := make([]string, 0, len(snippets))
	for path := range snippets {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	trimmedReason := strings.TrimSpace(reason)
	if trimmedReason == "" && len(paths) > 0 {
		trimmedReason = normalizeSnippetReason("", paths)
	}
	if trimmedReason == "" {
		b.WriteString("## File Context\n\n")
	} else {
		b.WriteString(fmt.Sprintf("## File Context: %s\n\n", trimmedReason))
	}
	warnings := []string{}
	retrieved := []string{}
	for _, path := range paths {
		ranges := snippets[path]
		if len(ranges) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: no snippets returned", path))
			continue
		}
		retrieved = append(retrieved, path)
		b.WriteString(fmt.Sprintf("### %s\n", path))
		resolvedPath, err := resolveReferencePath(path, repoRoot)
		if err != nil {
			warn := fmt.Sprintf("%s: invalid path (%v)", path, err)
			warnings = append(warnings, warn)
			b.WriteString(fmt.Sprintf("Warning: %s\n\n", warn))
			continue
		}
		lines, err := loadFileLinesForTag(resolvedPath)
		if err != nil {
			warn := fmt.Sprintf("%s: failed to read file (%v)", path, err)
			warnings = append(warnings, warn)
			b.WriteString(fmt.Sprintf("Warning: %s\n\n", warn))
			continue
		}
		lang := contextbuilder.DetectFenceLanguage(path)
		for i, r := range ranges {
			start := r.Start
			end := r.End
			if start <= 0 || end <= 0 || end < start {
				warn := fmt.Sprintf("%s: invalid range %d-%d", path, start, end)
				warnings = append(warnings, warn)
				b.WriteString(fmt.Sprintf("Warning: %s\n\n", warn))
				continue
			}
			if start > len(lines) {
				warn := fmt.Sprintf("%s: start line %d exceeds file length %d", path, start, len(lines))
				warnings = append(warnings, warn)
				b.WriteString(fmt.Sprintf("Warning: %s\n\n", warn))
				continue
			}
			if end > len(lines) {
				end = len(lines)
			}
			b.WriteString(fmt.Sprintf("Lines %d-%d:\n", start, end))
			b.WriteString("```")
			if lang != "" {
				b.WriteString(lang)
			}
			b.WriteString("\n")
			for line := start; line <= end; line++ {
				b.WriteString(fmt.Sprintf("%d: %s\n", line, lines[line-1]))
			}
			b.WriteString("```\n")
			if i < len(ranges)-1 {
				b.WriteString("\n")
			}
		}
		if len(ranges) > 0 {
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n"), retrieved, warnings
}

func formatFullFileFallbackWithRoot(filepaths []string, repoRoot string, err error) (string, []string, []string) {
	paths := normalizeShowFilePaths(filepaths)
	var b strings.Builder
	b.WriteString("## File Context (full file fallback)\n\n")
	if err != nil {
		b.WriteString(fmt.Sprintf("Warning: snippet-discovery failed (%s). Showing full files instead.\n\n", strings.TrimSpace(err.Error())))
	}
	if len(paths) == 0 {
		return strings.TrimRight(b.String(), "\n"), nil, []string{"no valid file paths"}
	}
	warnings := []string{}
	retrieved := []string{}
	for _, path := range paths {
		retrieved = append(retrieved, path)
		b.WriteString(fmt.Sprintf("### %s\n", path))
		resolvedPath, rerr := resolveReferencePath(path, repoRoot)
		if rerr != nil {
			warn := fmt.Sprintf("%s: invalid path (%v)", path, rerr)
			warnings = append(warnings, warn)
			b.WriteString(fmt.Sprintf("Warning: %s\n\n", warn))
			continue
		}
		lines, rerr := loadFileLinesForTag(resolvedPath)
		if rerr != nil {
			warn := fmt.Sprintf("%s: failed to read file (%v)", path, rerr)
			warnings = append(warnings, warn)
			b.WriteString(fmt.Sprintf("Warning: %s\n\n", warn))
			continue
		}
		lang := contextbuilder.DetectFenceLanguage(path)
		b.WriteString("```")
		if lang != "" {
			b.WriteString(lang)
		}
		b.WriteString("\n")
		for idx, line := range lines {
			b.WriteString(fmt.Sprintf("%d: %s\n", idx+1, line))
		}
		b.WriteString("```\n\n")
	}
	return strings.TrimRight(b.String(), "\n"), retrieved, warnings
}

func normalizeShowFilePaths(paths []string) []string {
	seen := map[string]struct{}{}
	cleaned := []string{}
	for _, path := range paths {
		normalized := normalizeShowFilePath(path)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		cleaned = append(cleaned, normalized)
		if len(cleaned) >= maxShowFilePaths {
			break
		}
	}
	return cleaned
}

func normalizeShowFilePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "./")
	if filepath.IsAbs(trimmed) {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return cleaned
}

func normalizeSnippetReason(reason string, filepaths []string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed != "" {
		return trimmed
	}
	if len(filepaths) == 1 {
		return fmt.Sprintf("Show relevant context for %s", filepaths[0])
	}
	return "Show relevant context for requested files"
}

func missingSnippetFiles(requested []string, snippets map[string][]LineRange) []string {
	missing := []string{}
	for _, path := range requested {
		if _, ok := snippets[path]; ok {
			continue
		}
		missing = append(missing, path)
	}
	return missing
}

func invokeSnippetDiscovery(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (string, error) {
	if len(filepaths) == 0 {
		return "", errors.New("no file paths provided")
	}
	args := []string{"-r", reason}
	for _, path := range filepaths {
		args = append(args, "-f", path)
	}
	if strings.TrimSpace(modelAlias) != "" {
		args = append(args, "--model", strings.TrimSpace(modelAlias))
	}
	args = appendSnippetDiscoveryAPIKeyArgs(args, apiKeyOverrides)
	if verbose {
		args = append(args, "-v")
	}
	cmd := snippetDiscoveryCommandContext(ctx, "snippet-discovery", args...)
	cmd.Env = append(os.Environ(), workspaceRootEnv(repoRoot)...)
	cmd.Stdin = strings.NewReader("")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)
	logSnippetDiscoveryInvocation(ctx, reason, filepaths, repoRoot, duration, err, stderr.String(), args)
	if err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText != "" {
			return "", fmt.Errorf("snippet-discovery failed: %w: %s", err, errText)
		}
		return "", fmt.Errorf("snippet-discovery failed: %w", err)
	}
	return stdout.String(), nil
}

func appendSnippetDiscoveryAPIKeyArgs(args []string, apiKeyOverrides map[string]string) []string {
	if len(apiKeyOverrides) == 0 {
		return args
	}
	providers := make([]string, 0, len(apiKeyOverrides))
	trimmed := make(map[string]string, len(apiKeyOverrides))
	for provider, key := range apiKeyOverrides {
		p := strings.TrimSpace(provider)
		k := strings.TrimSpace(key)
		if p == "" || k == "" {
			continue
		}
		if _, exists := trimmed[p]; exists {
			continue
		}
		providers = append(providers, p)
		trimmed[p] = k
	}
	sort.Strings(providers)
	for _, provider := range providers {
		args = append(args, "--api-key", fmt.Sprintf("%s:%s", provider, trimmed[provider]))
	}
	return args
}

func workspaceRootEnv(repoRoot string) []string {
	trimmed := strings.TrimSpace(repoRoot)
	if trimmed == "" {
		return nil
	}
	return []string{fmt.Sprintf("MACHTIANI_WORKSPACE_ROOT=%s", trimmed)}
}

func logSnippetDiscoveryInvocation(ctx context.Context, reason string, filepaths []string, repoRoot string, duration time.Duration, runErr error, stderr string, args []string) {
	payload := map[string]any{
		"event_version": 1,
		"reason":        reason,
		"files":         append([]string(nil), filepaths...),
		"file_count":    len(filepaths),
		"duration_ms":   duration.Milliseconds(),
		"repo_root_set": strings.TrimSpace(repoRoot) != "",
		"command":       "snippet-discovery",
		"args":          append([]string(nil), args...),
	}
	if trimmed := strings.TrimSpace(stderr); trimmed != "" {
		payload["stderr_preview"] = trimSnippetDiscoveryText(trimmed, 400)
	}
	evt := trajectory.Event{Kind: "snippet_discovery", Payload: payload}
	if parentID, ok := trajectory.ParentSpanID(ctx); ok {
		evt.ParentSpanID = parentID
	}
	if runErr != nil {
		evt.Level = "error"
		evt.Err = &trajectory.ErrorInfo{Message: runErr.Error()}
	}
	_ = trajectory.EmitFromContext(ctx, evt)
}

func trimSnippetDiscoveryText(value string, limit int) string {
	trimmed := strings.TrimSpace(value)
	if limit <= 0 || len(trimmed) <= limit {
		return trimmed
	}
	if limit <= 3 {
		return trimmed[:limit]
	}
	return trimmed[:limit-3] + "..."
}
