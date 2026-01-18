package discovery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	cfgpkg "github.com/tursomari/machtiani/agent/internal/snippet-discovery/internal/config"
)

// Chat API types
type chatMessage struct {
	Role    string
	Content string
}

// LLMSettings holds runtime model configuration.
type LLMSettings struct {
	Model            llm.ResolvedModel
	Extras           map[string]any
	FallbackAliases  []string
	FallbackResolved []llm.ResolvedModel
	APIKeyOverrides  map[string]string
}

type showCommand struct {
	Paths []string
}

type showStats struct {
	Files     int
	Lines     int
	Truncated bool
}

type lineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type snippetOutput map[string][]lineRange

var reShowBlock = regexp.MustCompile(`(?s)<show>(.*?)</show>`)

const maxFinalRetries = 2

func readAllStdin(limit int) (string, bool, error) {
	var buf bytes.Buffer
	r := bufio.NewReader(os.Stdin)
	truncated := false
	for {
		chunk := make([]byte, 32*1024)
		n, err := r.Read(chunk)
		if n > 0 {
			if limit > 0 && buf.Len()+n > limit {
				remain := limit - buf.Len()
				if remain > 0 {
					buf.Write(chunk[:remain])
				}
				truncated = true
				for err == nil {
					_, err = r.Read(chunk)
				}
				if errors.Is(err, io.EOF) {
					return buf.String(), truncated, nil
				}
				return buf.String(), truncated, nil
			}
			buf.Write(chunk[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return buf.String(), truncated, err
		}
	}
	return buf.String(), truncated, nil
}

func normalizeRelPath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("path is empty")
	}
	if strings.Contains(trimmed, "\\") {
		return "", errors.New("path contains backslash")
	}
	if filepath.IsAbs(trimmed) {
		return "", errors.New("path must be relative")
	}
	if strings.HasSuffix(trimmed, "/") {
		return "", errors.New("path must be a file (no trailing slash)")
	}
	cleaned := filepath.ToSlash(filepath.Clean(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path must not contain ..")
	}
	return cleaned, nil
}

func normalizeInputPaths(paths []string) ([]string, map[string]struct{}, error) {
	seen := map[string]struct{}{}
	var normalized []string
	for _, raw := range paths {
		cleaned, err := normalizeRelPath(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid path %q: %w", raw, err)
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		st, err := os.Stat(filepath.FromSlash(cleaned))
		if err != nil {
			return nil, nil, fmt.Errorf("path %q not readable: %w", raw, err)
		}
		if st.IsDir() {
			return nil, nil, fmt.Errorf("path %q is a directory", raw)
		}
		f, err := os.Open(filepath.FromSlash(cleaned))
		if err != nil {
			return nil, nil, fmt.Errorf("path %q not readable: %w", raw, err)
		}
		_ = f.Close()
		seen[cleaned] = struct{}{}
		normalized = append(normalized, cleaned)
	}
	return normalized, seen, nil
}

func parseShowBlock(content string) (showCommand, bool, string) {
	matches := reShowBlock.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return showCommand{}, false, ""
	}
	if len(matches) > 1 {
		return showCommand{}, true, "multiple <show> blocks found"
	}
	match := matches[0]
	if len(match) < 4 {
		return showCommand{}, true, "invalid <show> block"
	}
	if strings.TrimSpace(content[:match[0]]) != "" || strings.TrimSpace(content[match[1]:]) != "" {
		return showCommand{}, true, "<show> must be the only content in the message"
	}
	inner := content[match[2]:match[3]]
	lines := strings.Split(inner, "\n")
	var paths []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		paths = append(paths, trimmed)
	}
	if len(paths) == 0 {
		return showCommand{}, true, "<show> must include at least one file path"
	}
	return showCommand{Paths: paths}, true, ""
}

func extractJSONObjectRange(text string) (int, int, bool) {
	start := strings.IndexByte(text, '{')
	if start == -1 {
		return 0, 0, false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return 0, 0, false
			}
			depth--
			if depth == 0 {
				return start, i + 1, true
			}
		}
	}
	return 0, 0, false
}

func parseFinalOutput(content string) (snippetOutput, string, bool, error) {
	start, end, ok := extractJSONObjectRange(content)
	if !ok {
		return nil, "", false, nil
	}
	if strings.TrimSpace(content[:start]) != "" || strings.TrimSpace(content[end:]) != "" {
		return nil, "", true, errors.New("output must be only JSON")
	}
	trimmed := strings.TrimSpace(content[start:end])
	var output snippetOutput
	if err := json.Unmarshal([]byte(trimmed), &output); err != nil {
		return nil, "", true, fmt.Errorf("invalid JSON: %w", err)
	}
	if output == nil {
		return nil, "", true, errors.New("JSON output must be an object mapping file paths")
	}
	return output, trimmed, true, nil
}

func countFileLines(path string) (int, error) {
	data, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, nil
	}
	count := bytes.Count(data, []byte("\n"))
	if data[len(data)-1] != '\n' {
		count++
	}
	return count, nil
}

func validateFinalOutput(output snippetOutput, allowed map[string]struct{}) (snippetOutput, error) {
	normalized := make(snippetOutput)
	lineCounts := map[string]int{}
	for rawPath, ranges := range output {
		cleaned, err := normalizeRelPath(rawPath)
		if err != nil {
			return nil, fmt.Errorf("invalid path %q: %w", rawPath, err)
		}
		if _, ok := allowed[cleaned]; !ok {
			return nil, fmt.Errorf("path not in allow-list: %s", cleaned)
		}
		if _, ok := lineCounts[cleaned]; !ok {
			count, err := countFileLines(cleaned)
			if err != nil {
				return nil, fmt.Errorf("read file %s: %w", cleaned, err)
			}
			lineCounts[cleaned] = count
		}
		count := lineCounts[cleaned]
		for i, r := range ranges {
			if r.Start <= 0 || r.End <= 0 {
				return nil, fmt.Errorf("range %d in %s must use positive line numbers", i+1, cleaned)
			}
			if r.End < r.Start {
				return nil, fmt.Errorf("range %d in %s has end before start", i+1, cleaned)
			}
			if count == 0 {
				return nil, fmt.Errorf("range %d in %s exceeds empty file", i+1, cleaned)
			}
			if r.End > count {
				return nil, fmt.Errorf("range %d in %s exceeds file bounds (%d lines)", i+1, cleaned, count)
			}
		}
		normalized[cleaned] = append(normalized[cleaned], ranges...)
	}
	return normalized, nil
}

func readShowLines(path string, maxLines int) ([]string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	var lines []string
	truncated := false
	lineCount := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				break
			}
		}
		line = strings.TrimRight(line, "\n")
		lineCount++
		if maxLines <= 0 || lineCount <= maxLines {
			lines = append(lines, line)
		} else {
			truncated = true
			break
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return lines, truncated, nil
}

func (cmd showCommand) Execute(maxLines int, allowed map[string]struct{}) (string, showStats, error) {
	var outputs []string
	stats := showStats{}
	seen := map[string]struct{}{}
	for _, rawPath := range cmd.Paths {
		cleaned, err := normalizeRelPath(rawPath)
		if err != nil {
			return "", stats, fmt.Errorf("invalid path %q: %w", rawPath, err)
		}
		if _, ok := allowed[cleaned]; !ok {
			return "", stats, fmt.Errorf("path not in allow-list: %s", cleaned)
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		lines, truncated, err := readShowLines(filepath.FromSlash(cleaned), maxLines)
		if err != nil {
			return "", stats, err
		}
		numbered := formatNumberedLines(lines, 1)
		truncMsg := ""
		if maxLines > 0 {
			truncMsg = fmt.Sprintf("[TRUNCATED: %d lines limit]", maxLines)
		}
		formatted := formatOutputBlock(numbered, outputBlockOptions{
			header:            fmt.Sprintf("FILE_CONTENT[%s]:\n", cleaned),
			footer:            "END_FILE_CONTENT\n",
			truncationMessage: truncMsg,
			maxLines:          maxLines,
		}, truncated)
		outputs = append(outputs, formatted.formatted)
		stats.Files++
		stats.Lines += formatted.linesIncluded
		if formatted.truncated {
			stats.Truncated = true
		}
	}
	return strings.Join(outputs, "\n"), stats, nil
}

func callChat(ctx context.Context, llmCfg LLMSettings, msgs []chatMessage) (string, error) {
	if strings.TrimSpace(llmCfg.Model.APIKey) == "" || strings.TrimSpace(llmCfg.Model.BaseURL) == "" || strings.TrimSpace(llmCfg.Model.Model) == "" {
		return "", errors.New("llm runtime not configured")
	}
	llmMsgs := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		llmMsgs[i] = llm.Message{Role: m.Role, Content: m.Content}
	}
	extras := cloneExtras(llmCfg.Extras)
	if _, ok := extras["temperature"]; !ok {
		extras["temperature"] = 0.2
	}
	if _, ok := extras["max_completion_tokens"]; !ok {
		extras["max_completion_tokens"] = 2048
	}
	chatCtx := llm.WithAPIKeyOverrides(ctx, llmCfg.APIKeyOverrides)
	return llm.ChatWithResolvedFallback(chatCtx, llmCfg.Model, llmCfg.FallbackAliases, llmCfg.FallbackResolved, extras, llmMsgs)
}

var chatInvoker = callChat

func cloneExtras(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func applyWorkspaceRoot(lg cfgpkg.Logger, tr *cfgpkg.TrajectoryRecorder) (func(), error) {
	root := strings.TrimSpace(os.Getenv("MACHTIANI_WORKSPACE_ROOT"))
	if root == "" {
		if tmpRoot := strings.TrimSpace(os.Getenv("MACHTIANI_TMP_ROOT")); tmpRoot != "" {
			root = filepath.Join(tmpRoot, "repo")
		}
	}
	if root == "" {
		return func() {}, nil
	}
	st, err := os.Stat(root)
	if err != nil {
		lg.Warn(fmt.Sprintf("workspace root missing at %s (%v); falling back", root, err))
		return func() {}, nil
	}
	if !st.IsDir() {
		lg.Warn(fmt.Sprintf("workspace root %s is not a directory; falling back", root))
		return func() {}, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	if err := os.Chdir(root); err != nil {
		return nil, err
	}
	if tr != nil && tr.Enabled {
		tr.Event("workspace_root", 0, map[string]any{"root": root})
	}
	return func() { _ = os.Chdir(wd) }, nil
}

func Run(ctx context.Context, cfg cfgpkg.Config, llmCfg LLMSettings) int {
	log.SetOutput(os.Stderr)
	log.SetFlags(0)

	lg := cfgpkg.Logger{JSON: cfg.LogJSON, V: cfg.Verbose}
	if strings.TrimSpace(cfg.Reason) == "" {
		lg.Error("reason is required")
		fmt.Fprintln(os.Stderr, "reason is required")
		return 2
	}
	if len(cfg.FilePaths) == 0 {
		lg.Error("at least one file path is required")
		fmt.Fprintln(os.Stderr, "at least one file path is required")
		return 2
	}
	if cfg.MaxRounds <= 0 {
		cfg.MaxRounds = 10
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 60
	}
	if cfg.MaxLinesPerFile <= 0 {
		cfg.MaxLinesPerFile = 500
	}
	if cfg.MaxTranscript <= 0 {
		cfg.MaxTranscript = 300000
	}

	tr := cfgpkg.TrajectoryRecorder{}
	if !cfg.NoTrajectory {
		path := strings.TrimSpace(cfg.TrajectoryPath)
		if path == "" {
			if envPath := strings.TrimSpace(os.Getenv("SNIPPET_DISCOVERY_TRAJECTORY")); envPath != "" {
				path = envPath
			} else {
				path = cfgpkg.AutoTrajectoryPath(os.Getpid())
			}
		}
		_ = tr.Start(path)
	}
	defer tr.Close()

	restoreWD, err := applyWorkspaceRoot(lg, &tr)
	if err != nil {
		lg.Error("failed to apply workspace root")
		fmt.Fprintln(os.Stderr, "failed to apply workspace root:", err)
		return 1
	}
	defer restoreWD()

	filePaths, allowList, err := normalizeInputPaths(cfg.FilePaths)
	if err != nil {
		lg.Error("invalid file paths")
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if tr.Enabled {
		cwd, _ := os.Getwd()
		tr.Event("run_start", 0, map[string]any{"cfg": cfgpkg.RedactConfig(cfg), "cwd": cwd})
	}

	transcript, truncated, err := readAllStdin(cfg.MaxTranscript)
	if err != nil {
		lg.Warn("failed to read stdin transcript")
	}

	messages := []chatMessage{{Role: "system", Content: buildSystemPrompt()}}
	userMsg := buildInitialUserMessage(cfg.Reason, filePaths, transcript, truncated)
	if userMsg != "" {
		messages = append(messages, chatMessage{Role: "user", Content: userMsg})
	}

	transcriptBytes := 0
	for _, msg := range messages {
		transcriptBytes += len(msg.Content)
	}

	for round := 1; round <= cfg.MaxRounds; round++ {
		if tr.Enabled {
			tr.Event("round_start", round, map[string]any{"round": round, "transcript_bytes": transcriptBytes, "messages": len(messages)})
			tr.Event("llm_request", round, map[string]any{"messages": messages})
		}

		llmCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
		assistantContent, err := chatInvoker(llmCtx, llmCfg, messages)
		cancel()
		if err != nil {
			lg.Error("chat API error")
			fmt.Fprintln(os.Stderr, "chat API error:", err)
			if tr.Enabled {
				tr.Event("run_end", round, map[string]any{"exit_code": 1, "reason": "chat_error"})
			}
			return 1
		}
		if tr.Enabled {
			tr.Event("llm_response", round, map[string]any{"content": assistantContent})
		}
		transcriptBytes += len(assistantContent)

		showCmd, hasShow, reject := parseShowBlock(assistantContent)
		if hasShow {
			if reject != "" {
				msg := buildShowRejectMessage(reject)
				messages = append(messages, chatMessage{Role: "user", Content: msg})
				transcriptBytes += len(msg)
				if tr.Enabled {
					tr.Event("show_rejected", round, map[string]any{"reason": reject})
				}
				continue
			}
			output, stats, err := showCmd.Execute(cfg.MaxLinesPerFile, allowList)
			if err != nil {
				msg := buildShowRejectMessage(err.Error())
				messages = append(messages, chatMessage{Role: "user", Content: msg})
				transcriptBytes += len(msg)
				if tr.Enabled {
					tr.Event("show_error", round, map[string]any{"error": err.Error()})
				}
				continue
			}
			messages = append(messages, chatMessage{Role: "user", Content: output})
			transcriptBytes += len(output)
			if tr.Enabled {
				tr.Event("show_output", round, map[string]any{"files": stats.Files, "lines": stats.Lines, "truncated": stats.Truncated})
			}
			continue
		}

		parsed, _, hasJSON, err := parseFinalOutput(assistantContent)
		if !hasJSON {
			msg := buildToolCallMissingMessage()
			messages = append(messages, chatMessage{Role: "user", Content: msg})
			transcriptBytes += len(msg)
			if tr.Enabled {
				tr.Event("final_missing", round, map[string]any{"reason": "no_json"})
			}
			continue
		}
		if err != nil {
			msg := buildFinalizeRejectMessage(err.Error())
			messages = append(messages, chatMessage{Role: "user", Content: msg})
			transcriptBytes += len(msg)
			if tr.Enabled {
				tr.Event("final_invalid", round, map[string]any{"error": err.Error()})
			}
			continue
		}
		normalized, err := validateFinalOutput(parsed, allowList)
		if err != nil {
			msg := buildFinalizeRejectMessage(err.Error())
			messages = append(messages, chatMessage{Role: "user", Content: msg})
			transcriptBytes += len(msg)
			if tr.Enabled {
				tr.Event("final_invalid", round, map[string]any{"error": err.Error()})
			}
			continue
		}
		orderedKeys := make([]string, 0, len(normalized))
		for k := range normalized {
			orderedKeys = append(orderedKeys, k)
		}
		sort.Strings(orderedKeys)
		final := make(snippetOutput)
		for _, k := range orderedKeys {
			final[k] = normalized[k]
		}
		payload, err := json.MarshalIndent(final, "", "  ")
		if err != nil {
			lg.Error("failed to marshal JSON output")
			fmt.Fprintln(os.Stderr, "failed to marshal JSON output:", err)
			return 1
		}
		if _, err := fmt.Fprintln(os.Stdout, string(payload)); err != nil {
			lg.Error("failed to write JSON output")
		}
		if tr.Enabled {
			tr.Event("final_output", round, map[string]any{"paths": orderedKeys})
			tr.Event("run_end", round, map[string]any{"exit_code": 0, "reason": "success"})
		}
		return 0
	}

	forcedMsg := buildForcedFinalizeMessage()
	for retry := 0; retry < maxFinalRetries; retry++ {
		messages = append(messages, chatMessage{Role: "user", Content: forcedMsg})
		transcriptBytes += len(forcedMsg)
		if tr.Enabled {
			tr.Event("forced_finalization", cfg.MaxRounds+retry+1, map[string]any{"retry": retry + 1})
			tr.Event("llm_request", cfg.MaxRounds+retry+1, map[string]any{"messages": messages})
		}
		llmCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSec)*time.Second)
		assistantContent, err := chatInvoker(llmCtx, llmCfg, messages)
		cancel()
		if err != nil {
			lg.Error("chat API error during forced finalization")
			fmt.Fprintln(os.Stderr, "chat API error:", err)
			continue
		}
		if tr.Enabled {
			tr.Event("llm_response", cfg.MaxRounds+retry+1, map[string]any{"content": assistantContent})
		}
		parsed, _, hasJSON, err := parseFinalOutput(assistantContent)
		if !hasJSON || err != nil {
			continue
		}
		normalized, err := validateFinalOutput(parsed, allowList)
		if err != nil {
			continue
		}
		payload, err := json.MarshalIndent(normalized, "", "  ")
		if err != nil {
			continue
		}
		if _, err := fmt.Fprintln(os.Stdout, string(payload)); err != nil {
			lg.Error("failed to write JSON output")
		}
		if tr.Enabled {
			tr.Event("final_output", cfg.MaxRounds+retry+1, map[string]any{"paths": len(normalized)})
			tr.Event("run_end", cfg.MaxRounds+retry+1, map[string]any{"exit_code": 0, "reason": "forced_success"})
		}
		return 0
	}

	lg.Warn("no valid JSON output produced")
	if tr.Enabled {
		tr.Event("run_end", cfg.MaxRounds+maxFinalRetries+1, map[string]any{"exit_code": 1, "reason": "no_final_output"})
	}
	return 1
}
