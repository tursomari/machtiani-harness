package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	patcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	"github.com/tursomari/machtiani/agent/internal/parser"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/patchlog"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

const (
	strictPatchMaxTranscriptRunes     = 8000
	strictPatchMaxPlannerPayloadRunes = 4000
	strictPatchMaxFileLines           = 800
	strictPatchMaxPatchAttempts       = 3
	strictPatchMaxPathAttempts        = 3
)

type ErrRewriteNotSupported struct {
	Message    string
	Reason     string
	Suggestion string
}

func (e *ErrRewriteNotSupported) Error() string {
	return fmt.Sprintf("%s: %s (%s)", e.Message, e.Reason, e.Suggestion)
}

const strictPatchSchemaExample = `{
  "edits": [
    {
      "path": "repo/relative/path.ext",
      "mode": "patch",
      "start_line": 42,
      "end_line": 45,
      "new_content": "replacement line 1\nreplacement line 2\n"
    }
  ],
  "metadata": {
    "description": "Short summary of the edit"
  }
}`

var (
	strictPatchLineRangeRegexp  = regexp.MustCompile(`\b(\d+)[-,](\d+)\b`)
	strictPatchTurnHeaderRegexp = regexp.MustCompile(`(?m)^== TURN \d+`)
)

var errStrictPatchEmptyChoices = errors.New("strict patch: llm returned no choices")

type strictPatchPathSelection struct {
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}

type strictPatchContextKind string

const (
	strictPatchContextBaselineDiff strictPatchContextKind = "baseline_diff"
	strictPatchContextNumbered     strictPatchContextKind = "numbered"
)

type strictPatchFileContext struct {
	Body      string
	Kind      strictPatchContextKind
	Truncated bool
}

func (c *Client) runStrictPatchFlow(ctx context.Context, goal, transcript string, step, maxSteps int, plannerPayload string) (string, error) {
	if strings.TrimSpace(c.cfg.RepoRoot) == "" {
		return "", errors.New("planner: repo root required for strict patch mode")
	}
	trimmedTranscript := trimStrictPatchTranscript(transcript)
	selection, err := c.strictPatchSelectPath(ctx, goal, trimmedTranscript, plannerPayload, step, maxSteps)
	if err != nil {
		return "", err
	}

	fileContent, exists, err := c.readRepoFile(selection.Path)
	if err != nil {
		return "", err
	}

	fileCtx, err := c.buildStrictPatchFileContext(selection.Path, fileContent, strictPatchMaxFileLines)
	if err != nil {
		return "", err
	}

	patchJSON, err := c.strictPatchGeneratePatch(ctx, goal, trimmedTranscript, plannerPayload, selection, fileContent, fileCtx, exists, step, maxSteps)
	if err != nil {
		return "", err
	}

	return patchJSON, nil
}

func (c *Client) strictPatchSelectPath(ctx context.Context, goal, transcript, plannerPayload string, step, maxSteps int) (strictPatchPathSelection, error) {
	prompt := c.strictPatchPathPrompt(goal, transcript, plannerPayload)
	var lastErr error
	for attempt := 0; attempt < strictPatchMaxPathAttempts; attempt++ {
		resp, err := c.callStrictPatchLLM(ctx, "planner.strict_patch_path", prompt, step, maxSteps)
		if err != nil {
			if errors.Is(err, errStrictPatchEmptyChoices) {
				lastErr = err
				continue
			}
			return strictPatchPathSelection{}, err
		}

		jsonBytes, err := parser.ExtractPatchJSONPayload(resp)
		if err != nil {
			return strictPatchPathSelection{}, fmt.Errorf("strict patch path: %w", err)
		}

		var selection strictPatchPathSelection
		if err := json.Unmarshal(jsonBytes, &selection); err != nil {
			return strictPatchPathSelection{}, fmt.Errorf("strict patch path decode: %w", err)
		}

		selection.Path = strings.TrimSpace(selection.Path)
		if selection.Path == "" {
			return strictPatchPathSelection{}, errors.New("strict patch path: empty path returned by model")
		}

		normalized, err := (patcher.Edit{Path: selection.Path}).NormalizedPath(c.cfg.RepoRoot)
		if err != nil {
			return strictPatchPathSelection{}, fmt.Errorf("strict patch path normalization: %w", err)
		}
		selection.Path = normalized

		if c.cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[planner] strict patch selected path: %s\n", selection.Path)
		}

		return selection, nil
	}
	if lastErr != nil {
		return strictPatchPathSelection{}, lastErr
	}
	return strictPatchPathSelection{}, errors.New("strict patch path: unable to obtain selection after retries")
}

func (c *Client) strictPatchGeneratePatch(ctx context.Context, goal, transcript, plannerPayload string, selection strictPatchPathSelection, fileContent string, fileCtx strictPatchFileContext, exists bool, step, maxSteps int) (string, error) {
	currentFileContent := fileContent
	currentContext := fileCtx
	currentExists := exists
	currentTruncated := fileCtx.Truncated

	basePrompt := c.strictPatchPatchPrompt(goal, transcript, plannerPayload, selection, currentContext, currentExists)
	prompt := basePrompt
	var lastErr error
	reloaded := false
	maybeForceReload := func(err error) (bool, error) {
		if err == nil {
			return false, nil
		}
		if !currentContext.Truncated || reloaded {
			return false, nil
		}
		if !strictPatchShouldForceReload(err, strictPatchMaxFileLines) {
			return false, nil
		}
		freshContent, freshExists, readErr := c.readRepoFile(selection.Path)
		if readErr != nil {
			return false, readErr
		}
		currentFileContent = freshContent
		currentExists = freshExists
		freshCtx, ctxErr := c.buildStrictPatchFileContext(selection.Path, currentFileContent, 0)
		if ctxErr != nil {
			return false, ctxErr
		}
		currentContext = freshCtx
		currentTruncated = currentContext.Truncated
		basePrompt = c.strictPatchPatchPrompt(goal, transcript, plannerPayload, selection, currentContext, currentExists)
		prompt = basePrompt
		reloaded = true
		lastErr = nil
		return true, nil
	}

	for attempt := 0; attempt < strictPatchMaxPatchAttempts; attempt++ {
		resp, err := c.callStrictPatchLLM(ctx, "planner.strict_patch_patch", prompt, step, maxSteps)
		if err != nil {
			if errors.Is(err, errStrictPatchEmptyChoices) {
				lastErr = err
				continue
			}
			return "", err
		}

		jsonBytes, err := parser.ExtractPatchJSONPayload(resp)
		if err != nil {
			lastErr = fmt.Errorf("strict patch payload: %w", err)
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		sanitizedJSON, err := preValidateStrictPatchJSON(jsonBytes, c.cfg.PatchFull)
		if err != nil {
			lastErr = fmt.Errorf("strict patch payload pre-validate: %w", err)
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		if c.cfg.PatchFull && isRewritePayload(sanitizedJSON) {
			return string(sanitizedJSON), nil
		}

		if strictPatchRequestsFullReload(sanitizedJSON) {
			if !currentTruncated {
				lastErr = errors.New("strict patch payload requested full reload despite full context")
				prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
				continue
			}
			if reloaded {
				lastErr = errors.New("strict patch payload requested full reload multiple times")
				prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
				continue
			}

			freshContent, freshExists, readErr := c.readRepoFile(selection.Path)
			if readErr != nil {
				return "", readErr
			}
			currentFileContent = freshContent
			currentExists = freshExists
			freshCtx, ctxErr := c.buildStrictPatchFileContext(selection.Path, currentFileContent, 0)
			if ctxErr != nil {
				return "", ctxErr
			}
			currentContext = freshCtx
			currentTruncated = currentContext.Truncated
			basePrompt = c.strictPatchPatchPrompt(goal, transcript, plannerPayload, selection, currentContext, currentExists)
			prompt = basePrompt
			reloaded = true
			lastErr = nil
			attempt--
			continue
		}

		normalizedJSON, err := c.normalizeStrictPatchJSON(sanitizedJSON, selection.Path, currentFileContent)
		if err != nil {
			lastErr = fmt.Errorf("strict patch payload normalize: %w", err)
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		var instr patcher.Instructions
		if err := json.Unmarshal(normalizedJSON, &instr); err != nil {
			lastErr = fmt.Errorf("strict patch payload decode: %w", err)
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		if len(instr.Edits) != 1 {
			lastErr = fmt.Errorf("strict patch planner: expected exactly one edit, got %d", len(instr.Edits))
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		edit := &instr.Edits[0]
		pathNormalized, err := edit.NormalizedPath(c.cfg.RepoRoot)
		if err != nil {
			lastErr = fmt.Errorf("strict patch planner: invalid edit path: %w", err)
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}
		if pathNormalized != selection.Path {
			lastErr = fmt.Errorf("strict patch planner: edit path %s does not match selected path %s", pathNormalized, selection.Path)
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		if edit.Mode != patcher.ModePatch {
			lastErr = fmt.Errorf("strict patch planner: expected mode %q, got %q", patcher.ModePatch, edit.Mode)
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		start := edit.StartLine
		end := edit.EndLine
		if start < 1 {
			lastErr = fmt.Errorf("strict patch planner: start_line must be >= 1 (got %d)", start)
		} else if end < 0 {
			lastErr = fmt.Errorf("strict patch planner: end_line must be >= 0 (got %d)", end)
		} else if end != 0 && end < start {
			lastErr = fmt.Errorf("strict patch planner: end_line %d precedes start_line %d", end, start)
		} else if end == 0 && strings.TrimSpace(edit.NewContent) == "" {
			lastErr = errors.New("strict patch planner: insertion requires non-empty new_content")
		} else {
			lastErr = nil
		}

		if lastErr != nil {
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		encoded, err := json.MarshalIndent(instr, "", "  ")
		if err != nil {
			lastErr = fmt.Errorf("strict patch planner: encode: %w", err)
			if reloadedNow, reloadErr := maybeForceReload(lastErr); reloadErr != nil {
				return "", reloadErr
			} else if reloadedNow {
				attempt--
				continue
			}
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}
		return string(encoded), nil
	}

	if lastErr == nil {
		lastErr = errors.New("strict patch planner: unable to obtain valid patch after retries")
	}
	return "", lastErr
}

func (c *Client) strictPatchRetryPrompt(base string, err error) string {
	reason := sanitizeForPrompt(err.Error())
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\nPrevious attempt failed because: ")
	b.WriteString(reason)
	b.WriteString("\nRemember the strict patch schema structure:\n")
	b.WriteString(strictPatchSchemaExample)
	b.WriteString("\nReturn only the corrected JSON patch object now.")
	return b.String()
}

func strictPatchShouldForceReload(err error, truncateLimit int) bool {
	if err == nil {
		return false
	}
	if truncateLimit <= 0 {
		return false
	}
	msg := err.Error()
	if !(strings.Contains(msg, "line range") || strings.Contains(msg, "range out of bounds") || strings.Contains(msg, "insert out of bounds") || strings.Contains(msg, "exceed file bounds") || strings.Contains(msg, "exceeds file line count")) {
		return false
	}
	matches := strictPatchLineRangeRegexp.FindAllStringSubmatch(msg, -1)
	for _, match := range matches {
		if len(match) != 3 {
			continue
		}
		start, err1 := strconv.Atoi(match[1])
		end, err2 := strconv.Atoi(match[2])
		if err1 != nil || err2 != nil {
			continue
		}
		if start > truncateLimit || end > truncateLimit {
			return true
		}
	}
	if strings.Contains(msg, "exceed file bounds") || strings.Contains(msg, "exceeds file line count") {
		return true
	}
	return false
}

func preValidateStrictPatchJSON(raw []byte, allowRewrite bool) ([]byte, error) {
	sanitized, changed := sanitizeStrictPatchJSONStringLiterals(raw)
	if changed {
		raw = sanitized
	}

	var scratch map[string]any
	if err := json.Unmarshal(raw, &scratch); err != nil {
		return nil, err
	}
	if strictPatchRequestsFullReload(raw) {
		return raw, nil
	}
	if len(scratch) == 0 {
		return nil, errors.New("strict patch: expected JSON object with \"edits\"")
	}

	editsVal, ok := scratch["edits"]
	if !ok {
		return nil, errors.New("strict patch: missing \"edits\" array")
	}
	editsSlice, ok := toAnySlice(editsVal)
	if !ok {
		return nil, errors.New("strict patch: \"edits\" must be an array")
	}
	if len(editsSlice) == 0 {
		return nil, errors.New("strict patch: \"edits\" array must contain exactly one entry")
	}

	for idx, rawEdit := range editsSlice {
		editMap, ok := toStringMap(rawEdit)
		if !ok {
			return nil, fmt.Errorf("strict patch: edits[%d] must be an object", idx)
		}
		if idx == 0 {
			path := strings.TrimSpace(getString(editMap["path"]))
			if path == "" {
				return nil, errors.New("strict patch: edits[0].path must be a non-empty string")
			}
			mode := strings.TrimSpace(strings.ToLower(getString(editMap["mode"])))
			if mode != "" && mode != string(patcher.ModePatch) {
				if mode == string(patcher.ModeRewrite) {
					if !allowRewrite {
						return nil, &ErrRewriteNotSupported{
							Message:    "Strict patch mode does not support full-file rewrites",
							Reason:     "Rewrites require unconstrained file replacement; strict mode enforces explicit line ranges",
							Suggestion: "Use non-strict mode (--patch-strict=false) or full-mode (--patch-full) for rewrites",
						}
					}
					// If allowed, we accept it as-is (skipping strict validation).
					return raw, nil
				}
				return nil, fmt.Errorf("strict patch: edits[0].mode must be %q", patcher.ModePatch)
			}
			if _, ok := editMap["start_line"]; !ok {
				return nil, errors.New("strict patch: edits[0].start_line is required")
			}
			if _, hasEnd := editMap["end_line"]; !hasEnd {
				if _, hasRange := editMap["line_range"]; !hasRange {
					return nil, errors.New("strict patch: edits[0].end_line is required (use 0 for insertions)")
				}
			}
			if !(hasKey(editMap, "new_content") || hasKey(editMap, "new_lines") || hasKey(editMap, "replacement") || hasKey(editMap, "new_text") || hasKey(editMap, "after") || hasKey(editMap, "text")) {
				return nil, errors.New("strict patch: edits[0] must include new_content (string) or new_lines array")
			}
		}
	}

	return raw, nil
}

func strictPatchRequestsFullReload(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}

	val, ok := payload["request_full_reload"]
	if !ok {
		return false
	}

	switch v := val.(type) {
	case bool:
		return v
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return v != 0
	case json.Number:
		i64, err := v.Int64()
		if err != nil {
			return false
		}
		return i64 != 0
	default:
		return false
	}
}

func sanitizeStrictPatchJSONStringLiterals(input []byte) ([]byte, bool) {
	if len(input) == 0 {
		return input, false
	}

	var buf bytes.Buffer
	buf.Grow(len(input))

	inString := false
	escaped := false
	changed := false

	for _, b := range input {
		if inString {
			if escaped {
				buf.WriteByte(b)
				escaped = false
				continue
			}

			switch b {
			case '\\':
				buf.WriteByte(b)
				escaped = true
				continue
			case '"':
				buf.WriteByte(b)
				inString = false
				continue
			case '\t':
				buf.WriteByte('\\')
				buf.WriteByte('t')
				changed = true
				continue
			case '\n':
				buf.WriteByte('\\')
				buf.WriteByte('n')
				changed = true
				continue
			case '\r':
				buf.WriteByte('\\')
				buf.WriteByte('r')
				changed = true
				continue
			}

			if b < 0x20 {
				buf.WriteString(fmt.Sprintf("\\u%04x", b))
				changed = true
				continue
			}

			buf.WriteByte(b)
			continue
		}

		buf.WriteByte(b)
		if b == '"' {
			inString = true
			escaped = false
		}
	}

	if !changed {
		return input, false
	}

	return buf.Bytes(), true
}

func hasKey(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	_, ok := m[key]
	return ok
}

func extractRequiredInt(m map[string]any, keys []string) (int, error) {
	for _, key := range keys {
		if val, ok := m[key]; ok {
			if ptr, err := toIntValue(val); err != nil {
				return 0, err
			} else if ptr != nil {
				return *ptr, nil
			}
		}
	}
	if val, ok := m["line_range"]; ok {
		start, _, err := parseLineRange(val)
		return start, err
	}
	return 0, fmt.Errorf("missing %s", keys[0])
}

func extractEndLine(m map[string]any, start int) (int, error) {
	if val, ok := m["end_line"]; ok {
		ptr, err := toIntValue(val)
		if err != nil {
			return 0, err
		}
		if ptr == nil {
			return 0, fmt.Errorf("end_line missing value")
		}
		return *ptr, nil
	}
	if val, ok := m["line_range"]; ok {
		startRange, endRange, err := parseLineRange(val)
		if err != nil {
			return 0, err
		}
		if start <= 0 {
			start = startRange
		}
		return endRange, nil
	}
	if val, ok := m["end"]; ok {
		ptr, err := toIntValue(val)
		if err != nil {
			return 0, err
		}
		if ptr != nil {
			return *ptr, nil
		}
	}
	if start == 0 {
		return 0, nil
	}
	return start, nil
}

func parseLineRange(val any) (int, int, error) {
	switch v := val.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, 0, fmt.Errorf("empty line_range")
		}
		tokens := splitRangeTokens(s)
		if len(tokens) == 1 {
			n, err := strconv.Atoi(tokens[0])
			if err != nil {
				return 0, 0, err
			}
			return n, n, nil
		}
		if len(tokens) >= 2 {
			start, err1 := strconv.Atoi(tokens[0])
			end, err2 := strconv.Atoi(tokens[1])
			if err1 != nil || err2 != nil {
				if err1 != nil {
					return 0, 0, err1
				}
				return 0, 0, err2
			}
			return start, end, nil
		}
	case []any:
		if len(v) == 0 {
			return 0, 0, fmt.Errorf("empty line_range")
		}
		startPtr, err := toIntValue(v[0])
		if err != nil {
			return 0, 0, err
		}
		if startPtr == nil {
			return 0, 0, fmt.Errorf("line_range start missing")
		}
		endPtr := startPtr
		if len(v) > 1 {
			candidate, err := toIntValue(v[1])
			if err != nil {
				return 0, 0, err
			}
			if candidate == nil {
				return 0, 0, fmt.Errorf("line_range end missing")
			}
			endPtr = candidate
		}
		return *startPtr, *endPtr, nil
	}
	return 0, 0, fmt.Errorf("unsupported line_range type %T", val)
}

func splitRangeTokens(raw string) []string {
	replaced := strings.NewReplacer(",", " ", "-", " ", "[", "", "]", "").Replace(raw)
	return strings.Fields(replaced)
}

func extractNewContent(edit map[string]any) (string, error) {
	stringKeys := []string{"new_content", "content", "text", "replacement", "new_text", "after"}
	for _, key := range stringKeys {
		if val, ok := edit[key]; ok {
			if val == nil {
				return "", nil
			}
			if s, ok := toStringValue(val); ok {
				return s, nil
			}
			return "", fmt.Errorf("strict patch: %s must be a string", key)
		}
	}

	lineKeys := []string{"new_lines", "lines"}
	for _, key := range lineKeys {
		if val, ok := edit[key]; ok {
			lines, err := toStringSlice(val)
			if err != nil {
				return "", fmt.Errorf("strict patch: %s: %w", key, err)
			}
			return joinLines(lines), nil
		}
	}

	return "", errors.New("strict patch: new_content missing")
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func (c *Client) normalizeStrictPatchJSON(raw []byte, expectedPath string, fileContent string) ([]byte, error) {
	_ = fileContent
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	var instr patcher.Instructions
	if metaVal, ok := payload["metadata"]; ok && metaVal != nil {
		metaBytes, err := json.Marshal(metaVal)
		if err != nil {
			return nil, fmt.Errorf("strict patch: encode metadata: %w", err)
		}
		var meta patcher.Metadata
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			return nil, fmt.Errorf("strict patch: decode metadata: %w", err)
		}
		instr.Metadata = &meta
	}

	editsVal, ok := payload["edits"]
	if !ok {
		return nil, errors.New("strict patch: missing edits array")
	}
	editsSlice, ok := toAnySlice(editsVal)
	if !ok || len(editsSlice) == 0 {
		return nil, errors.New("strict patch: edits must contain exactly one entry")
	}
	if len(editsSlice) != 1 {
		return nil, fmt.Errorf("strict patch: expected exactly one edit, found %d", len(editsSlice))
	}

	editMap, ok := toStringMap(editsSlice[0])
	if !ok {
		return nil, errors.New("strict patch: edit must be an object")
	}

	pathVal := strings.TrimSpace(getString(editMap["path"]))
	if pathVal == "" {
		pathVal = expectedPath
	}
	if pathVal == "" {
		return nil, errors.New("strict patch: path is required")
	}

	mode := strings.TrimSpace(strings.ToLower(getString(editMap["mode"])))
	if mode != "" && mode != string(patcher.ModePatch) {
		return nil, fmt.Errorf("strict patch: unsupported mode %q", mode)
	}

	startLine, err := extractRequiredInt(editMap, []string{"start_line", "line"})
	if err != nil {
		return nil, fmt.Errorf("strict patch: invalid start_line: %w", err)
	}
	if startLine < 0 {
		return nil, errors.New("strict patch: start_line must be >= 0")
	}

	endLine, err := extractEndLine(editMap, startLine)
	if err != nil {
		return nil, fmt.Errorf("strict patch: invalid end_line: %w", err)
	}

	newContent, err := extractNewContent(editMap)
	if err != nil {
		return nil, err
	}

	instr.Edits = append(instr.Edits, patcher.Edit{
		Path:       pathVal,
		Mode:       patcher.ModePatch,
		StartLine:  startLine,
		EndLine:    endLine,
		NewContent: newContent,
	})

	return json.MarshalIndent(instr, "", "  ")
}

func (c *Client) callStrictPatchLLM(ctx context.Context, baseEvent string, prompt string, step, maxSteps int) (string, error) {
	w, hasWriter := trajectory.FromContext(ctx)
	parentSpan, _ := trajectory.ParentSpanID(ctx)
	callCtx := ctx
	var span trajectory.Span
	if hasWriter {
		span = w.StartSpan(parentSpan)
		payload := map[string]any{
			"event_version": 1,
			"model_alias":   c.cfg.Alias,
			"model_name":    c.cfg.Model.Model,
			"step":          step,
			"max_steps":     maxSteps,
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(prompt, w.ExcerptLen()), "prompt")
		if err := w.Emit(ctx, trajectory.Event{Kind: baseEvent + ".request", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}); err != nil {
			reportTrajectoryError(err)
		}
		callCtx = trajectory.ContextWithParentSpan(ctx, span.ID)
	}
	if strings.Contains(baseEvent, "strict_patch") {
		meta := patchlog.Metadata{
			Source: baseEvent,
			Model:  strings.TrimSpace(c.cfg.Model.Model),
			Alias:  strings.TrimSpace(c.cfg.Alias),
			Step:   step,
		}
		if path, err := patchlog.WritePrompt(prompt, meta); err != nil {
			if c.cfg.Verbose {
				fmt.Fprintf(os.Stderr, "[planner] failed to write %s prompt log: %v\n", baseEvent, err)
			}
		} else if c.cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[planner] %s prompt logged to %s\n", baseEvent, path)
		}
	}

	start := time.Now()
	resp, err := c.chat(callCtx, prompt)
	duration := time.Since(start)
	if err != nil {
		if hasWriter {
			payload := map[string]any{
				"event_version": 1,
				"model_alias":   c.cfg.Alias,
				"model_name":    c.cfg.Model.Model,
				"step":          step,
				"max_steps":     maxSteps,
				"duration_ms":   duration.Milliseconds(),
				"parse_ok":      false,
			}
			category, code := llm.ClassifyError(err)
			evt := trajectory.Event{
				Level:        "error",
				Kind:         baseEvent + ".response",
				SpanID:       span.ID,
				ParentSpanID: parentSpan,
				Payload:      payload,
				Err: &trajectory.ErrorInfo{
					Message:  err.Error(),
					Category: category,
					Code:     code,
				},
			}
			if emitErr := w.Emit(ctx, evt); emitErr != nil {
				reportTrajectoryError(emitErr)
			}
		}
		if errors.Is(err, llm.ErrNoChoices) {
			return "", fmt.Errorf("%w: %s", errStrictPatchEmptyChoices, err.Error())
		}
		return "", err
	}

	if hasWriter {
		payload := map[string]any{
			"event_version": 1,
			"model_alias":   c.cfg.Alias,
			"model_name":    c.cfg.Model.Model,
			"step":          step,
			"max_steps":     maxSteps,
			"duration_ms":   duration.Milliseconds(),
			"parse_ok":      true,
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(prompt, w.ExcerptLen()), "prompt")
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(resp, w.ExcerptLen()), "response")
		evt := trajectory.Event{Kind: baseEvent + ".response", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}
		if emitErr := w.Emit(ctx, evt); emitErr != nil {
			reportTrajectoryError(emitErr)
		}
	}

	if c.cfg.Verbose {
		fmt.Fprintf(os.Stderr, "[planner] %s response: %s\n", baseEvent, truncateMiddle(strings.TrimSpace(resp), 1800))
	}

	return resp, nil
}

func (c *Client) strictPatchPathPrompt(goal, transcript, plannerPayload string) string {
	var b strings.Builder
	b.WriteString("You are the strict patch path selector for mct.\n")
	b.WriteString("Read the goal and transcript to determine the single repository file that must be edited next.\n")
	b.WriteString("Output only a single JSON object of the form {\"path\": \"repo/relative/file\", \"reason\": \"...\"}.\n")
	b.WriteString("Rules: path must be repo-relative, use forward slashes, and point to a file (not a directory).\n")
	b.WriteString("Do not emit markdown fences or commentary—JSON only.\n\n")

	if trimmed := strings.TrimSpace(plannerPayload); trimmed != "" {
		b.WriteString("Earlier planner output (for context):\n")
		b.WriteString(truncateForPrompt(trimmed, strictPatchMaxPlannerPayloadRunes))
		b.WriteString("\n\n")
	}

	appendSuccessFilesSection(&b, c.progress.SuccessFiles, "Files already patched successfully this session—skip re-selecting these paths unless you just reloaded them and found fresh issues:\n", successFilesPromptLimit)

	if trimmed := strings.TrimSpace(goal); trimmed != "" {
		b.WriteString("Goal:\n")
		b.WriteString(truncateForPrompt(trimmed, strictPatchMaxTranscriptRunes))
		b.WriteString("\n\n")
	}

	if trimmed := strings.TrimSpace(transcript); trimmed != "" {
		b.WriteString("Transcript:\n")
		b.WriteString(truncateForPrompt(trimmed, strictPatchMaxTranscriptRunes))
		b.WriteString("\n\n")
	}

	b.WriteString("Respond with JSON now.")
	return b.String()
}

func (c *Client) strictPatchPatchPrompt(goal, transcript, plannerPayload string, selection strictPatchPathSelection, fileCtx strictPatchFileContext, exists bool) string {
	var b strings.Builder
	b.WriteString("You are the strict patch planner for mct.\n")
	b.WriteString("The previous step already selected the target file path. Generate patch instructions that modify only that file.\n")
	b.WriteString("Output a single JSON object matching the strict patch schema. No markdown fences, no commentary.\n\n")
	b.WriteString("Strict patch JSON must follow this structure (fill in the real values):\n")
	b.WriteString(strictPatchSchemaExample)
	b.WriteString("\n\n")

	b.WriteString("Selected path: ")
	b.WriteString(selection.Path)
	b.WriteString("\n")
	if reason := strings.TrimSpace(selection.Reason); reason != "" {
		b.WriteString("Selection rationale: ")
		b.WriteString(reason)
		b.WriteString("\n")
	}
	if exists {
		b.WriteString("File status: exists on disk.\n")
	} else {
		b.WriteString("File status: not found on disk. Only insertions (end_line = 0) are valid until the file exists.\n")
	}
	if fileCtx.Kind == strictPatchContextNumbered && fileCtx.Truncated {
		b.WriteString("Note: file display truncated after ")
		b.WriteString(strconv.Itoa(strictPatchMaxFileLines))
		b.WriteString(" lines—reload the file before patching if more context is required.\n")
		b.WriteString("If the range you need (start_line/end_line) is truncated from view, respond with JSON field {\"request_full_reload\": true} instead of guessing—do NOT proceed with incomplete data.\n")
	}
	b.WriteString("\n")

	b.WriteString("Patch requirements:\n")
	b.WriteString("- Produce exactly one edit with mode \"patch\" targeting the selected path.\n")
	b.WriteString("- Supply 1-based \"start_line\" and \"end_line\" for the line range to splice; use end_line = 0 for pure insertions.\n")
	b.WriteString("- Place the replacement snippet in \"new_content\". Leave it empty for deletions.\n")
	b.WriteString("- Preserve indentation and include any trailing newline you expect in the file.\n")
	b.WriteString("- Keep edits tightly scoped so follow-up turns can adjust other regions if required.\n")
	b.WriteString("- If the numbered view is truncated and you need more context, respond with {\"request_full_reload\": true}.\n")
	b.WriteString("- Populate metadata.description with a short summary.\n\n")

	appendSuccessFilesSection(&b, c.progress.SuccessFiles, "Files already patched successfully this session (reload them before attempting more edits; prefer untouched files):\n", successFilesPromptLimit)

	if trimmed := strings.TrimSpace(plannerPayload); trimmed != "" {
		b.WriteString("Earlier planner output (for context):\n")
		b.WriteString(truncateForPrompt(trimmed, strictPatchMaxPlannerPayloadRunes))
		b.WriteString("\n\n")
	}

	if trimmed := strings.TrimSpace(goal); trimmed != "" {
		b.WriteString("Goal:\n")
		b.WriteString(truncateForPrompt(trimmed, strictPatchMaxTranscriptRunes))
		b.WriteString("\n\n")
	}

	if trimmed := strings.TrimSpace(transcript); trimmed != "" {
		b.WriteString("Transcript:\n")
		b.WriteString(truncateForPrompt(trimmed, strictPatchMaxTranscriptRunes))
		b.WriteString("\n\n")
	}

	switch fileCtx.Kind {
	case strictPatchContextBaselineDiff:
		b.WriteString("Baseline-relative diff (session baseline vs current workspace). Line numbers on the left reflect the current file state; use them for start_line/end_line selection.\n")
		b.WriteString(fileCtx.Body)
		if !strings.HasSuffix(fileCtx.Body, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n")
	default:
		b.WriteString("File contents with 1-based line numbers (prefix \"N |\" is not part of the file):\n")
		b.WriteString("```text\n")
		b.WriteString(fileCtx.Body)
		b.WriteString("```\n\n")
	}

	b.WriteString("Return only the JSON patch object now.")
	return b.String()
}

func trimStrictPatchTranscript(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	matches := strictPatchTurnHeaderRegexp.FindAllStringIndex(trimmed, -1)
	if len(matches) == 0 {
		return trimmed
	}
	type turnSegment struct {
		content  string
		decision string
	}
	turns := make([]turnSegment, 0, len(matches))
	for i, loc := range matches {
		start := loc[0]
		end := len(trimmed)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		segment := strings.TrimSpace(trimmed[start:end])
		if segment == "" {
			continue
		}
		turns = append(turns, turnSegment{content: segment, decision: extractStrictPatchDecision(segment)})
	}
	if len(turns) == 0 {
		return trimmed
	}
	filtered := make([]turnSegment, 0, len(turns))
	for _, turn := range turns {
		if strings.EqualFold(strings.TrimSpace(turn.decision), "background") {
			continue
		}
		filtered = append(filtered, turn)
	}
	var keep []turnSegment
	if len(filtered) == 0 {
		keep = turns[len(turns)-1:]
	} else {
		keep = filtered[len(filtered)-1:]
	}
	var b strings.Builder
	for _, turn := range keep {
		segment := strings.TrimSpace(turn.content)
		if segment == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(segment)
	}
	result := strings.TrimSpace(b.String())
	if result == "" {
		return trimmed
	}
	return result
}

func extractStrictPatchDecision(segment string) string {
	const marker = "Planner decision:"
	idx := strings.Index(segment, marker)
	if idx < 0 {
		return ""
	}
	rest := segment[idx+len(marker):]
	rest = strings.TrimSpace(rest)
	if newline := strings.IndexByte(rest, '\n'); newline >= 0 {
		rest = rest[:newline]
	}
	return strings.TrimSpace(rest)
}

func truncateForPrompt(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if maxRunes <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes < 5 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-5]) + "\n..."
}

func sanitizeForPrompt(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func toAnySlice(val any) ([]any, bool) {
	switch v := val.(type) {
	case []any:
		return v, true
	default:
		return nil, false
	}
}

func toStringMap(val any) (map[string]any, bool) {
	m, ok := val.(map[string]any)
	return m, ok
}

func toStringValue(val any) (string, bool) {
	switch v := val.(type) {
	case string:
		return v, true
	case fmt.Stringer:
		return v.String(), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

func getString(val any) string {
	if s, ok := toStringValue(val); ok {
		return s
	}
	return ""
}

func toStringSlice(val any) ([]string, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case []string:
		return append([]string(nil), v...), nil
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := toStringValue(item)
			if !ok {
				return nil, fmt.Errorf("expected string elements")
			}
			out = append(out, splitLinesPreserve(s)...)
		}
		return out, nil
	case string:
		return splitLinesPreserve(v), nil
	default:
		if s, ok := toStringValue(v); ok {
			return splitLinesPreserve(s), nil
		}
		return nil, fmt.Errorf("unsupported type %T", val)
	}
}

func splitLinesPreserve(val string) []string {
	normalized := strings.ReplaceAll(val, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	if normalized == "" {
		return []string{""}
	}
	endsWithNewline := strings.HasSuffix(normalized, "\n")
	if endsWithNewline {
		normalized = normalized[:len(normalized)-1]
	}
	lines := strings.Split(normalized, "\n")
	if endsWithNewline {
		lines = append(lines, "")
	}
	return lines
}

func toPositiveInt(val any) (int, error) {
	res, err := toIntValue(val)
	if err != nil {
		return 0, err
	}
	if res == nil {
		return 0, errors.New("missing value")
	}
	if *res <= 0 {
		return 0, errors.New("must be > 0")
	}
	return *res, nil
}

func toIntValue(val any) (*int, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case float64:
		i := int(v)
		if float64(i) != v {
			return nil, fmt.Errorf("not an integer")
		}
		return pointerOfInt(i), nil
	case json.Number:
		i64, err := v.Int64()
		if err != nil {
			return nil, err
		}
		i := int(i64)
		return pointerOfInt(i), nil
	case int:
		return pointerOfInt(v), nil
	case int64:
		i := int(v)
		return pointerOfInt(i), nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return nil, nil
		}
		i64, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, err
		}
		i := int(i64)
		return pointerOfInt(i), nil
	default:
		return nil, fmt.Errorf("cannot convert %T to int", val)
	}
}

func pointerOfInt(v int) *int {
	res := v
	return &res
}

func (c *Client) readRepoFile(rel string) (string, bool, error) {
	abs := filepath.Join(c.cfg.RepoRoot, filepath.FromSlash(rel))
	data, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("planner: unable to read %s: %w", rel, err)
	}
	return string(data), true, nil
}

func (c *Client) buildStrictPatchFileContext(relPath string, fileContent string, maxLines int) (strictPatchFileContext, error) {
	sessionID := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_ID"))
	if sessionID == "" {
		sessionID = strings.TrimSpace(c.cfg.SessionID)
	}
	if sessionID == "" {
		numbered, truncated := formatFileWithLineNumbers(fileContent, maxLines)
		return strictPatchFileContext{Body: numbered, Kind: strictPatchContextNumbered, Truncated: truncated}, nil
	}

	repoRoot := strings.TrimSpace(c.cfg.RepoRoot)
	if repoRoot == "" {
		return strictPatchFileContext{}, errors.New("strict patch: repo root required for baseline-relative diff")
	}

	baseline, err := patchersvc.EnsureBaseline(sessionID, repoRoot, time.Now())
	if err != nil {
		return strictPatchFileContext{}, fmt.Errorf("strict patch: ensure baseline for %s failed: %w", relPath, err)
	}
	if baseline == nil {
		return strictPatchFileContext{}, fmt.Errorf("strict patch: baseline unavailable for session %s", sessionID)
	}

	section, ok, err := patchersvc.BuildBaselineDiffSection(baseline, repoRoot, relPath)
	if err != nil {
		return strictPatchFileContext{}, fmt.Errorf("strict patch: build baseline diff for %s: %w", relPath, err)
	}
	if !ok {
		return strictPatchFileContext{}, fmt.Errorf("strict patch: no baseline diff generated for %s", relPath)
	}
	if !strings.HasSuffix(section, "\n") {
		section += "\n"
	}
	return strictPatchFileContext{Body: section, Kind: strictPatchContextBaselineDiff}, nil
}

func formatFileWithLineNumbers(content string, maxLines int) (string, bool) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	lineCount := len(lines)
	if lineCount > 0 && lines[lineCount-1] == "" {
		// Preserve trailing newline semantics without double blank line output.
		lineCount--
	}

	width := len(strconv.Itoa(lineCount))
	if width < 3 {
		width = 3
	}

	limit := lineCount
	truncated := false
	if maxLines > 0 && limit > maxLines {
		limit = maxLines
		truncated = true
	}

	var b strings.Builder
	for i := 0; i < limit; i++ {
		fmt.Fprintf(&b, "%*d | %s\n", width, i+1, lines[i])
	}
	if truncated {
		fmt.Fprintf(&b, "... (%d additional line(s) truncated)\n", lineCount-limit)
	}
	if lineCount == 0 {
		fmt.Fprintf(&b, "%*d | \n", width, 1)
	}
	return b.String(), truncated
}

func isRewritePayload(raw []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	editsVal, ok := payload["edits"]
	if !ok {
		return false
	}
	editsSlice, ok := toAnySlice(editsVal)
	if !ok || len(editsSlice) == 0 {
		return false
	}
	editMap, ok := toStringMap(editsSlice[0])
	if !ok {
		return false
	}
	mode := strings.TrimSpace(strings.ToLower(getString(editMap["mode"])))
	return mode == string(patcher.ModeRewrite)
}
