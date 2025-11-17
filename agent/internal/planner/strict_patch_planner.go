package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	patcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	"github.com/tursomari/machtiani/agent/internal/parser"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

const (
	strictPatchMaxTranscriptRunes     = 8000
	strictPatchMaxPlannerPayloadRunes = 4000
	strictPatchMaxFileLines           = 800
	strictPatchMaxPatchAttempts       = 3
	strictPatchMaxPathAttempts        = 3
)

var errStrictPatchEmptyChoices = errors.New("strict patch: llm returned no choices")

type strictPatchPathSelection struct {
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}

func (c *Client) runStrictPatchFlow(ctx context.Context, goal, transcript string, step, maxSteps int, plannerPayload string) (string, error) {
	if strings.TrimSpace(c.cfg.RepoRoot) == "" {
		return "", errors.New("planner: repo root required for strict patch mode")
	}

	selection, err := c.strictPatchSelectPath(ctx, goal, transcript, plannerPayload, step, maxSteps)
	if err != nil {
		return "", err
	}

	fileContent, exists, err := c.readRepoFile(selection.Path)
	if err != nil {
		return "", err
	}

	numbered, truncated := formatFileWithLineNumbers(fileContent)
	patchJSON, err := c.strictPatchGeneratePatch(ctx, goal, transcript, plannerPayload, selection, fileContent, numbered, exists, truncated, step, maxSteps)
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

func (c *Client) strictPatchGeneratePatch(ctx context.Context, goal, transcript, plannerPayload string, selection strictPatchPathSelection, fileContent, numberedContent string, exists, truncated bool, step, maxSteps int) (string, error) {
	basePrompt := c.strictPatchPatchPrompt(goal, transcript, plannerPayload, selection, numberedContent, exists, truncated)
	prompt := basePrompt
	var lastErr error

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
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		sanitizedJSON, err := preValidateStrictPatchJSON(jsonBytes)
		if err != nil {
			lastErr = fmt.Errorf("strict patch payload pre-validate: %w", err)
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		normalizedJSON, err := c.normalizeStrictPatchJSON(sanitizedJSON, selection.Path, fileContent)
		if err != nil {
			lastErr = fmt.Errorf("strict patch payload normalize: %w", err)
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		var instr patcher.Instructions
		if err := json.Unmarshal(normalizedJSON, &instr); err != nil {
			lastErr = fmt.Errorf("strict patch payload decode: %w", err)
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		if len(instr.Edits) != 1 {
			lastErr = fmt.Errorf("strict patch planner: expected exactly one edit, got %d", len(instr.Edits))
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		edit := &instr.Edits[0]
		pathNormalized, err := edit.NormalizedPath(c.cfg.RepoRoot)
		if err != nil {
			lastErr = fmt.Errorf("strict patch planner: invalid edit path: %w", err)
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}
		if pathNormalized != selection.Path {
			lastErr = fmt.Errorf("strict patch planner: edit path %s does not match selected path %s", pathNormalized, selection.Path)
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		if edit.Mode != patcher.ModePatch {
			lastErr = fmt.Errorf("strict patch planner: expected mode \"patch\", got %q", edit.Mode)
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}
		if edit.PatchInfo == nil {
			lastErr = errors.New("strict patch planner: missing patch payload (expected \"patch\": { \"hunks\": [...] })")
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}
		lastErr = nil
		for i := range edit.PatchInfo.Hunks {
			h := &edit.PatchInfo.Hunks[i]
			if h.SnippetSource == nil {
				lastErr = fmt.Errorf("strict patch planner: hunks[%d] missing snippet_source", i)
				break
			}
			if h.SnippetSource.StartLine <= 0 {
				lastErr = fmt.Errorf("strict patch planner: invalid snippet_source range in hunk[%d]", i)
				break
			}
			if h.SnippetSource.EndLine < h.SnippetSource.StartLine {
				isInsertionRange := h.SnippetSource.EndLine == h.SnippetSource.StartLine-1
				if !isInsertionRange || (len(h.ContextBefore)+len(h.Deletions)+len(h.ContextAfter)) != 0 {
					lastErr = fmt.Errorf("strict patch planner: invalid snippet_source range in hunk[%d]", i)
					break
				}
			}
			if strings.TrimSpace(h.SnippetSource.Filepath) != "" {
				normalizedSnippet, err := (patcher.Edit{Path: h.SnippetSource.Filepath}).NormalizedPath(c.cfg.RepoRoot)
				if err != nil {
					lastErr = fmt.Errorf("strict patch planner: hunks[%d] snippet_source filepath invalid: %v", i, err)
					break
				}
				if normalizedSnippet != selection.Path {
					lastErr = fmt.Errorf("strict patch planner: hunks[%d] snippet_source filepath %s differs from %s", i, normalizedSnippet, selection.Path)
					break
				}
			}

			contextBeforeLen := len(h.ContextBefore)
			expectedOld := contextBeforeLen + len(h.Deletions) + len(h.ContextAfter)
			expectedNew := contextBeforeLen + len(h.Additions) + len(h.ContextAfter)
			if h.OldCount != expectedOld {
				h.OldCount = expectedOld
			}
			if h.NewCount != expectedNew {
				h.NewCount = expectedNew
			}

			startLine := h.SnippetSource.StartLine
			if contextBeforeLen > 0 {
				adjustedStart := startLine - contextBeforeLen
				if adjustedStart < 1 {
					adjustedStart = 1
				}
				if h.SnippetSource.StartLine != adjustedStart {
					h.SnippetSource.StartLine = adjustedStart
				}
				startLine = adjustedStart
			}
			if h.OldStart != startLine {
				h.OldStart = startLine
			}
			if expectedOld == 0 {
				expectedEnd := startLine - 1
				if h.SnippetSource.EndLine != expectedEnd {
					h.SnippetSource.EndLine = expectedEnd
				}
			} else {
				expectedEnd := startLine + expectedOld - 1
				if h.SnippetSource.EndLine != expectedEnd {
					h.SnippetSource.EndLine = expectedEnd
				}
			}

			lastErr = nil
		}

		if lastErr != nil {
			prompt = c.strictPatchRetryPrompt(basePrompt, lastErr)
			continue
		}

		encoded, err := json.MarshalIndent(instr, "", "  ")
		if err != nil {
			lastErr = fmt.Errorf("strict patch planner: encode: %w", err)
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
	return base + "\nPrevious attempt failed because: " + reason + "\nReturn only the corrected JSON patch object now."
}

func preValidateStrictPatchJSON(raw []byte) ([]byte, error) {
	sanitized, changed := sanitizeStrictPatchJSONStringLiterals(raw)
	if changed {
		raw = sanitized
	}

	var scratch map[string]any
	if err := json.Unmarshal(raw, &scratch); err != nil {
		return nil, err
	}

	return raw, nil
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

func (c *Client) normalizeStrictPatchJSON(raw []byte, expectedPath string, fileContent string) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}

	editsVal, ok := payload["edits"]
	if !ok {
		return nil, errors.New("strict patch: missing edits array")
	}
	editsSlice, ok := toAnySlice(editsVal)
	if !ok {
		return nil, errors.New("strict patch: edits must be an array")
	}

	fileLines := splitFileLines(fileContent)
	for i := range editsSlice {
		editMap, ok := toStringMap(editsSlice[i])
		if !ok {
			return nil, fmt.Errorf("strict patch: edit[%d] must be an object", i)
		}

		if mode, _ := toStringValue(editMap["mode"]); strings.TrimSpace(strings.ToLower(mode)) == "" {
			editMap["mode"] = patcher.ModePatch
		}

		if path, _ := toStringValue(editMap["path"]); strings.TrimSpace(path) == "" {
			editMap["path"] = expectedPath
		}

		patchVal, hasPatch := editMap["patch"]
		if !hasPatch || patchVal == nil {
			if hunksVal, hasHunks := editMap["hunks"]; hasHunks {
				editMap["patch"] = map[string]any{"hunks": hunksVal}
				delete(editMap, "hunks")
			} else {
				return nil, fmt.Errorf("strict patch: edit[%d] missing patch.hunks", i)
			}
		}

		patchMap, ok := toStringMap(editMap["patch"])
		if !ok {
			return nil, fmt.Errorf("strict patch: edit[%d] patch must be an object", i)
		}

		hunksVal, ok := patchMap["hunks"]
		if !ok {
			return nil, fmt.Errorf("strict patch: edit[%d] patch missing hunks", i)
		}
		hunkSlice, ok := toAnySlice(hunksVal)
		if !ok {
			return nil, fmt.Errorf("strict patch: edit[%d] hunks must be an array", i)
		}

		for j := range hunkSlice {
			hunkMap, ok := toStringMap(hunkSlice[j])
			if !ok {
				return nil, fmt.Errorf("strict patch: edit[%d] hunk[%d] must be an object", i, j)
			}
			if err := normalizeStrictHunk(hunkMap, expectedPath, fileLines); err != nil {
				return nil, fmt.Errorf("strict patch: edit[%d] hunk[%d]: %w", i, j, err)
			}
			hunkSlice[j] = hunkMap
		}
		patchMap["hunks"] = hunkSlice
		editMap["patch"] = patchMap
		editsSlice[i] = editMap
	}
	payload["edits"] = editsSlice

	return json.Marshal(payload)
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

func (c *Client) strictPatchPatchPrompt(goal, transcript, plannerPayload string, selection strictPatchPathSelection, numberedContent string, exists, truncated bool) string {
	var b strings.Builder
	b.WriteString("You are the strict patch planner for mct.\n")
	b.WriteString("The previous step already selected the target file path. Generate patch instructions that modify only that file.\n")
	b.WriteString("Output a single JSON object matching the strict patch schema. No markdown fences, no commentary.\n\n")

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
		b.WriteString("File status: not found on disk (treat insertions carefully; old_count should be 0 for pure insertions).\n")
	}
	if truncated {
		b.WriteString("Note: file display truncated after ")
		b.WriteString(strconv.Itoa(strictPatchMaxFileLines))
		b.WriteString(" lines—reload the file before patching if more context is required.\n")
	}
	b.WriteString("\n")

	b.WriteString("Patch requirements:\n")
	b.WriteString("- Produce exactly one edit with mode \"patch\" targeting the selected path.\n")
	b.WriteString("- Each hunk must include snippet_source {start_line, end_line} for the original content.\n")
	b.WriteString("- Provide context_before/context_after plus additions/deletions; newline-separated strings are fine—we will split them.\n")
	b.WriteString("- Convenience fields like old_text/new_text or replacement are accepted; we normalize them into strict patch hunks.\n")
	b.WriteString("- snippet_source start/end must reference the original lines shown below (use the numeric prefixes).\n")
	b.WriteString("- Keep hunks tightly scoped. If inserting, set old_count=0 and snippet_source end_line = start_line - 1.\n")
	b.WriteString("- Do not introduce additional edits or mutate other files.\n")
	b.WriteString("- Populate metadata.description with a short summary.\n\n")

	if trimmed := strings.TrimSpace(plannerPayload); trimmed != "" {
		b.WriteString("Earlier planner output (for context):\n")
		b.WriteString(truncateForPrompt(trimmed, strictPatchMaxPlannerPayloadRunes))
		b.WriteString("\n\n")
	}
	appendSuccessFilesSection(&b, c.progress.SuccessFiles, "Files already patched successfully this session (reload them before attempting more edits; prefer untouched files):\n", successFilesPromptLimit)

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

	b.WriteString("File contents with 1-based line numbers (prefix \"N |\" is not part of the file):\n")
	b.WriteString("```text\n")
	b.WriteString(numberedContent)
	b.WriteString("```\n\n")

	b.WriteString("Return only the JSON patch object now.")
	return b.String()
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

func normalizeStrictHunk(h map[string]any, expectedPath string, fileLines []string) error {
	contextBefore, _, err := parseLineField(h, "context_before")
	if err != nil {
		return fmt.Errorf("context_before: %w", err)
	}
	if contextBefore == nil {
		contextBefore = []string{}
	}
	h["context_before"] = contextBefore

	contextAfter, _, err := parseLineField(h, "context_after")
	if err != nil {
		return fmt.Errorf("context_after: %w", err)
	}
	if contextAfter == nil {
		contextAfter = []string{}
	}
	h["context_after"] = contextAfter

	deletions, deletionsPresent, err := parseLineField(h, "deletions")
	if err != nil {
		return fmt.Errorf("deletions: %w", err)
	}

	additions, additionsPresent, err := parseLineField(h, "additions")
	if err != nil {
		return fmt.Errorf("additions: %w", err)
	}

	if !deletionsPresent {
		if alt, ok, err := consumeLineField(h, "old_text", "old_lines", "before", "original", "previous_text"); err != nil {
			return fmt.Errorf("old_text: %w", err)
		} else if ok {
			deletions = trimContextFromLines(alt, contextBefore, contextAfter)
		}
	}

	if !additionsPresent {
		if alt, ok, err := consumeLineField(h, "new_text", "new_lines", "after", "replacement", "updated_text", "next_text"); err != nil {
			return fmt.Errorf("new_text: %w", err)
		} else if ok {
			trimmed := trimContextFromLines(alt, contextBefore, contextAfter)
			if len(trimmed) == 0 && len(alt) > 0 {
				trimmed = alt
			}
			additions = trimmed
		}
	}

	snippetMap, ok := toStringMap(h["snippet_source"])
	if !ok {
		snippetMap = map[string]any{}
	}
	if _, ok := snippetMap["filepath"]; !ok {
		snippetMap["filepath"] = ""
	}

	startLine, err := toPositiveInt(snippetMap["start_line"])
	if err != nil {
		return fmt.Errorf("snippet_source.start_line: %w", err)
	}
	endLine, err := toIntValue(snippetMap["end_line"])
	if err != nil {
		return fmt.Errorf("snippet_source.end_line: %w", err)
	}
	if endLine == nil {
		endLine = pointerOfInt(startLine)
	}
	if *endLine < startLine-1 {
		return fmt.Errorf("snippet_source end_line must be >= start_line-1")
	}

	snippetMap["start_line"] = startLine
	snippetMap["end_line"] = *endLine
	if strings.TrimSpace(expectedPath) != "" {
		snippetMap["filepath"] = expectedPath
	} else {
		snippetMap["filepath"] = getString(snippetMap["filepath"])
	}
	h["snippet_source"] = snippetMap

	originalStart := startLine
	originalEnd := *endLine
	rawSnippet := extractSnippetLines(fileLines, startLine, *endLine)
	if rawSnippet == nil {
		realignedStart, realignedEnd, realignErr := realignSnippetSource(fileLines, startLine, *endLine, contextBefore, deletions, contextAfter)
		if realignErr != nil {
			return fmt.Errorf("snippet_source lines %d-%d exceed file bounds: %w", originalStart, originalEnd, realignErr)
		}
		startLine = realignedStart
		*endLine = realignedEnd
		snippetMap["start_line"] = startLine
		snippetMap["end_line"] = *endLine
		h["snippet_source"] = snippetMap
		rawSnippet = extractSnippetLines(fileLines, startLine, *endLine)
		if rawSnippet == nil {
			return fmt.Errorf("snippet_source lines %d-%d exceed file bounds after realignment", startLine, *endLine)
		}
	}

	if len(deletions) == 0 || len(additions) == 0 {
		if len(deletions) == 0 {
			derived := trimContextFromLines(rawSnippet, contextBefore, contextAfter)
			if len(derived) > 0 {
				deletions = derived
			}
		}
		if len(additions) == 0 {
			// For insertions where snippet range is empty we keep additions empty until other sources fill it.
		}
	}

	deletions = trimBoundaryBlanks(deletions, contextBefore, contextAfter)
	additions = trimBoundaryBlanks(additions, contextBefore, contextAfter)

	if len(deletions) == 0 && len(additions) == 0 {
		return errors.New("missing additions/deletions content")
	}

	contextBeforeLen := len(contextBefore)
	expectedOld := contextBeforeLen + len(deletions) + len(contextAfter)
	adjustedStart := startLine - contextBeforeLen
	if adjustedStart < 1 {
		adjustedStart = 1
	}
	adjustedEnd := adjustedStart + expectedOld - 1
	if expectedOld == 0 {
		adjustedEnd = adjustedStart - 1
	}
	finalSnippet := extractSnippetLines(fileLines, adjustedStart, adjustedEnd)
	if finalSnippet == nil {
		realignedStart, realignedEnd, realignErr := realignSnippetSource(fileLines, startLine, *endLine, contextBefore, deletions, contextAfter)
		if realignErr != nil {
			return fmt.Errorf("snippet_source lines %d-%d exceed file bounds", adjustedStart, adjustedEnd)
		}
		snippetMap["start_line"] = realignedStart
		snippetMap["end_line"] = realignedEnd
		h["snippet_source"] = snippetMap
		startLine = realignedStart
		*endLine = realignedEnd
		adjustedStart = startLine - contextBeforeLen
		if adjustedStart < 1 {
			adjustedStart = 1
		}
		adjustedEnd = adjustedStart + expectedOld - 1
		if expectedOld == 0 {
			adjustedEnd = adjustedStart - 1
		}
		finalSnippet = extractSnippetLines(fileLines, adjustedStart, adjustedEnd)
		if finalSnippet == nil {
			return fmt.Errorf("snippet_source lines %d-%d exceed file bounds", adjustedStart, adjustedEnd)
		}
	}
	snippetMap["start_line"] = adjustedStart
	snippetMap["end_line"] = adjustedEnd
	h["snippet_source"] = snippetMap
	startLine = adjustedStart
	*endLine = adjustedEnd

	if len(deletions) > 0 {
		expectedSnippet := make([]string, 0, len(contextBefore)+len(deletions)+len(contextAfter))
		expectedSnippet = append(expectedSnippet, contextBefore...)
		expectedSnippet = append(expectedSnippet, deletions...)
		expectedSnippet = append(expectedSnippet, contextAfter...)
		if !slicesEqualExact(finalSnippet, expectedSnippet) {
			pathLabel := expectedPath
			if strings.TrimSpace(pathLabel) == "" {
				pathLabel = "selected file"
			}
			return fmt.Errorf("snippet_source content mismatch for %s:%d-%d", pathLabel, startLine, *endLine)
		}
	}

	for _, alias := range []string{"replacement", "old_text", "old_lines", "before", "original", "previous_text", "new_text", "new_lines", "after", "updated_text", "next_text"} {
		delete(h, alias)
	}

	h["deletions"] = deletions
	h["additions"] = additions
	return nil
}

func realignSnippetSource(fileLines []string, startLine, endLine int, contextBefore, deletions, contextAfter []string) (int, int, error) {
	totalContext := len(contextBefore) + len(deletions) + len(contextAfter)
	if totalContext == 0 {
		return 0, 0, fmt.Errorf("anchor_missing: no context available to realign snippet")
	}
	approximateStart := startLine - 1
	if approximateStart < 0 {
		approximateStart = -1
	}
	hunk := patcher.Hunk{
		ContextBefore: contextBefore,
		Deletions:     deletions,
		ContextAfter:  contextAfter,
	}
	idx, _, _, reason, err := patcher.FindHunkMatch(fileLines, hunk, approximateStart)
	if err != nil {
		return 0, 0, fmt.Errorf("%s: %w", reason, err)
	}
	newStart := idx + 1
	newEnd := newStart + totalContext - 1
	return newStart, newEnd, nil
}

func parseLineField(obj map[string]any, key string) ([]string, bool, error) {
	val, ok := obj[key]
	if !ok {
		return nil, false, nil
	}
	lines, err := toStringSlice(val)
	if err != nil {
		return nil, true, err
	}
	if lines == nil {
		lines = []string{}
	}
	obj[key] = lines
	return lines, true, nil
}

func consumeLineField(obj map[string]any, keys ...string) ([]string, bool, error) {
	for _, key := range keys {
		val, ok := obj[key]
		if !ok {
			continue
		}
		lines, err := toStringSlice(val)
		if err != nil {
			return nil, false, err
		}
		delete(obj, key)
		return lines, true, nil
	}
	return nil, false, nil
}

func trimContextFromLines(lines, contextBefore, contextAfter []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, len(lines))
	copy(out, lines)
	if len(contextBefore) > 0 && len(out) >= len(contextBefore) && slicesEqualPrefix(out, contextBefore) {
		out = out[len(contextBefore):]
	}
	if len(contextAfter) > 0 && len(out) >= len(contextAfter) && slicesEqualSuffix(out, contextAfter) {
		out = out[:len(out)-len(contextAfter)]
	}
	return out
}

func trimBoundaryBlanks(lines, contextBefore, contextAfter []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := lines
	if len(out) > 0 && out[0] == "" && len(contextBefore) > 0 && contextBefore[len(contextBefore)-1] == "" {
		out = out[1:]
	}
	if len(out) > 0 && out[len(out)-1] == "" && len(contextAfter) > 0 && contextAfter[0] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func slicesEqualPrefix(lines, prefix []string) bool {
	if len(prefix) > len(lines) {
		return false
	}
	for i := range prefix {
		if lines[i] != prefix[i] {
			return false
		}
	}
	return true
}

func slicesEqualSuffix(lines, suffix []string) bool {
	if len(suffix) > len(lines) {
		return false
	}
	offset := len(lines) - len(suffix)
	for i := range suffix {
		if lines[offset+i] != suffix[i] {
			return false
		}
	}
	return true
}

func slicesEqualExact(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func splitFileLines(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func extractSnippetLines(lines []string, startLine, endLine int) []string {
	if startLine <= 0 {
		return nil
	}
	startIdx := startLine - 1
	if startIdx > len(lines) {
		return nil
	}
	if endLine < startLine-1 {
		return nil
	}
	if endLine < startLine {
		return []string{}
	}
	endIdx := endLine
	if endIdx > len(lines) {
		endIdx = len(lines)
	}
	if endIdx <= startIdx {
		return []string{}
	}
	dup := make([]string, endIdx-startIdx)
	copy(dup, lines[startIdx:endIdx])
	return dup
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

func formatFileWithLineNumbers(content string) (string, bool) {
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
	if limit > strictPatchMaxFileLines {
		limit = strictPatchMaxFileLines
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
