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
		parts = append(parts, fmt.Sprintf("invalid entries: %s", strings.Join(details, ", ")))
	}
	if len(parts) == 0 {
		return "snippet-discovery returned partial results"
	}
	return strings.Join(parts, "; ")
}

func (e *SnippetDiscoveryPartialError) Kinds() map[string]struct{} {
	if e == nil {
		return nil
	}
	kinds := map[string]struct{}{}
	if len(e.Missing) > 0 {
		kinds["missing_snippets"] = struct{}{}
	}
	for _, reason := range e.Invalid {
		kind := classifySnippetDiscoveryErrorText(reason)
		if kind == "" {
			continue
		}
		kinds[kind] = struct{}{}
	}
	if len(kinds) == 0 {
		return nil
	}
	return kinds
}

func SnippetDiscoveryReasonKinds(partial *SnippetDiscoveryPartialError) []string {
	kinds := partial.Kinds()
	if len(kinds) == 0 {
		return nil
	}
	ordered := make([]string, 0, len(kinds))
	for kind := range kinds {
		ordered = append(ordered, kind)
	}
	sort.Strings(ordered)
	return ordered
}

func SnippetDiscoveryErrorKind(err error) string {
	if err == nil {
		return ""
	}
	return classifySnippetDiscoveryErrorText(err.Error())
}

func classifySnippetDiscoveryRunError(runErr error, stderr string) string {
	if runErr == nil {
		return ""
	}
	if errors.Is(runErr, context.DeadlineExceeded) {
		return "timeout"
	}
	combined := strings.TrimSpace(stderr)
	if combined != "" {
		combined = combined + " " + runErr.Error()
	} else {
		combined = runErr.Error()
	}
	kind := classifySnippetDiscoveryErrorText(combined)
	if kind == "snippet_error" {
		if exitErr, ok := runErr.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			return "invalid_path"
		}
	}
	return kind
}

func classifySnippetDiscoveryErrorText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "context deadline exceeded"), strings.Contains(lower, "timeout"):
		return "timeout"
	case strings.Contains(lower, "chat api error"), strings.Contains(lower, "chat_api_error"):
		return "llm_error"
	case strings.Contains(lower, "final output parse error"), strings.Contains(lower, "final_parse_error"), strings.Contains(lower, "final output validation error"), strings.Contains(lower, "final_validate_error"), strings.Contains(lower, "invalid json"):
		return "parse_error"
	case strings.Contains(lower, "invalid range"), strings.Contains(lower, "range"):
		return "parse_error"
	case strings.Contains(lower, "file_path_error"), strings.Contains(lower, "invalid path"), strings.Contains(lower, "path not readable"), strings.Contains(lower, "no such file"), strings.Contains(lower, "is a directory"), strings.Contains(lower, "path must"), strings.Contains(lower, "path escapes repo root"):
		return "invalid_path"
	}
	return "snippet_error"
}

var (
	snippetDiscoveryCommandContext = exec.CommandContext
	runSnippetDiscovery            = invokeSnippetDiscovery
)

// FetchFileSnippets runs snippet-discovery with a model alias (typically the file-discovery model).
func FetchFileSnippets(ctx context.Context, detection ShowFileDetection, repoRoot, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (map[string][]LineRange, error) {
	rawPaths := append([]string(nil), detection.Filepaths...)
	if len(rawPaths) == 0 && strings.TrimSpace(detection.Filepath) != "" {
		rawPaths = []string{detection.Filepath}
	}
	filepaths, invalidPaths := preflightShowFilePaths(rawPaths, repoRoot)
	if len(filepaths) == 0 {
		if len(invalidPaths) > 0 {
			return nil, &SnippetDiscoveryPartialError{Invalid: invalidPaths}
		}
		return nil, errors.New("no file paths provided")
	}
	reason := detection.VerbatimQuestion
	if strings.TrimSpace(reason) == "" {
		reason = normalizeSnippetReason(detection.Reason, filepaths)
	}
	cleaned, partialErr, err := runSnippetDiscoveryWithRetry(ctx, repoRoot, reason, filepaths, modelAlias, apiKeyOverrides, verbose)
	if err != nil {
		return nil, err
	}
	if len(invalidPaths) > 0 {
		partialErr = mergeSnippetDiscoveryPartialErrors(partialErr, &SnippetDiscoveryPartialError{Invalid: invalidPaths})
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

func runSnippetDiscoveryWithRetry(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (map[string][]LineRange, *SnippetDiscoveryPartialError, error) {
	raw, err := runSnippetDiscovery(ctx, repoRoot, reason, filepaths, modelAlias, apiKeyOverrides, verbose)
	if err != nil {
		if len(filepaths) > 1 {
			cleaned, partialErr := retrySnippetDiscoveryByFile(ctx, repoRoot, reason, filepaths, modelAlias, apiKeyOverrides, verbose)
			return cleaned, partialErr, nil
		}
		return nil, nil, err
	}
	cleaned, partialErr, err := parseSnippetDiscoveryOutput(raw, filepaths)
	if err != nil {
		if len(filepaths) > 1 {
			cleaned, retryErr := retrySnippetDiscoveryByFile(ctx, repoRoot, reason, filepaths, modelAlias, apiKeyOverrides, verbose)
			return cleaned, retryErr, nil
		}
		return nil, nil, err
	}
	return cleaned, partialErr, nil
}

func retrySnippetDiscoveryByFile(ctx context.Context, repoRoot, reason string, filepaths []string, modelAlias string, apiKeyOverrides map[string]string, verbose bool) (map[string][]LineRange, *SnippetDiscoveryPartialError) {
	cleaned := map[string][]LineRange{}
	var partialErr *SnippetDiscoveryPartialError
	for _, path := range filepaths {
		raw, err := runSnippetDiscovery(ctx, repoRoot, reason, []string{path}, modelAlias, apiKeyOverrides, verbose)
		if err != nil {
			partialErr = mergeSnippetDiscoveryPartialErrors(partialErr, &SnippetDiscoveryPartialError{Invalid: map[string]string{path: trimSnippetDiscoveryText(err.Error(), 200)}})
			continue
		}
		parsed, parsedPartial, err := parseSnippetDiscoveryOutput(raw, []string{path})
		if err != nil {
			partialErr = mergeSnippetDiscoveryPartialErrors(partialErr, &SnippetDiscoveryPartialError{Invalid: map[string]string{path: trimSnippetDiscoveryText(err.Error(), 200)}})
			continue
		}
		for k, ranges := range parsed {
			cleaned[k] = ranges
		}
		if parsedPartial != nil {
			partialErr = mergeSnippetDiscoveryPartialErrors(partialErr, parsedPartial)
		}
	}
	return cleaned, partialErr
}

func parseSnippetDiscoveryOutput(raw string, requested []string) (map[string][]LineRange, *SnippetDiscoveryPartialError, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil, errors.New("snippet-discovery returned empty output")
	}
	var decoded map[string][]LineRange
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, nil, fmt.Errorf("parse snippet-discovery output: %w", err)
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
	missing := missingSnippetFiles(requested, cleaned)
	var partialErr *SnippetDiscoveryPartialError
	if len(missing) > 0 || len(invalid) > 0 {
		partialErr = &SnippetDiscoveryPartialError{Missing: missing, Invalid: invalid}
	}
	if partialErr != nil && len(partialErr.Missing) == 0 && len(partialErr.Invalid) == 0 {
		partialErr = nil
	}
	return cleaned, partialErr, nil
}

func mergeSnippetDiscoveryPartialErrors(parts ...*SnippetDiscoveryPartialError) *SnippetDiscoveryPartialError {
	var merged SnippetDiscoveryPartialError
	seenMissing := map[string]struct{}{}
	for _, part := range parts {
		if part == nil {
			continue
		}
		for _, missing := range part.Missing {
			if _, ok := seenMissing[missing]; ok {
				continue
			}
			seenMissing[missing] = struct{}{}
			merged.Missing = append(merged.Missing, missing)
		}
		if len(part.Invalid) > 0 {
			if merged.Invalid == nil {
				merged.Invalid = map[string]string{}
			}
			for path, reason := range part.Invalid {
				if _, ok := merged.Invalid[path]; ok {
					continue
				}
				merged.Invalid[path] = reason
			}
		}
	}
	if len(merged.Missing) == 0 && len(merged.Invalid) == 0 {
		return nil
	}
	return &merged
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
		trimmed := strings.TrimSpace(err.Error())
		var partialErr *SnippetDiscoveryPartialError
		if errors.As(err, &partialErr) {
			b.WriteString(fmt.Sprintf("Warning: snippet-discovery returned partial results (%s). Showing full files for remaining paths.\n\n", trimmed))
		} else {
			b.WriteString(fmt.Sprintf("Warning: snippet-discovery failed (%s). Showing full files instead.\n\n", trimmed))
		}
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
	normalized, _ := normalizeShowFilePathWithRoot(path, "")
	return normalized
}

func normalizeShowFilePathWithRoot(path, repoRoot string) (string, string) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", "path is empty"
	}
	trimmed = strings.TrimPrefix(trimmed, "./")
	if filepath.IsAbs(trimmed) {
		if strings.TrimSpace(repoRoot) == "" {
			return "", "path must be relative"
		}
		rel, err := filepath.Rel(repoRoot, trimmed)
		if err != nil {
			return "", "path must be within repo root"
		}
		if rel == "." || strings.HasPrefix(rel, "..") {
			return "", "path must be within repo root"
		}
		trimmed = rel
	}
	cleaned := filepath.ToSlash(filepath.Clean(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", "path must not contain .."
	}
	return cleaned, ""
}

func preflightShowFilePaths(paths []string, repoRoot string) ([]string, map[string]string) {
	seen := map[string]struct{}{}
	cleaned := []string{}
	invalid := map[string]string{}
	for _, path := range paths {
		normalized, reason := normalizeShowFilePathWithRoot(path, repoRoot)
		if reason != "" {
			recordInvalidPath(invalid, path, normalized, reason)
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		resolved, err := resolveReferencePath(normalized, repoRoot)
		if err != nil {
			recordInvalidPath(invalid, normalized, normalized, err.Error())
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			recordInvalidPath(invalid, normalized, normalized, fmt.Sprintf("path not readable: %v", err))
			continue
		}
		if info.IsDir() {
			recordInvalidPath(invalid, normalized, normalized, "path is a directory")
			continue
		}
		seen[normalized] = struct{}{}
		cleaned = append(cleaned, normalized)
		if len(cleaned) >= maxShowFilePaths {
			break
		}
	}
	if len(invalid) == 0 {
		invalid = nil
	}
	return cleaned, invalid
}

func PreflightShowFilePaths(paths []string, repoRoot string) ([]string, map[string]string) {
	return preflightShowFilePaths(paths, repoRoot)
}

func recordInvalidPath(invalid map[string]string, rawPath, normalizedPath, reason string) {
	if invalid == nil || reason == "" {
		return
	}
	key := normalizedPath
	if key == "" {
		key = strings.TrimSpace(rawPath)
	}
	if key == "" {
		key = rawPath
	}
	if key == "" {
		return
	}
	if _, ok := invalid[key]; ok {
		return
	}
	invalid[key] = reason
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
	if runErr != nil {
		if kind := classifySnippetDiscoveryRunError(runErr, stderr); kind != "" {
			payload["error_kind"] = kind
		}
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
