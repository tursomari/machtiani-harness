package transcript

import (
	"context"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

type Transcript struct {
	f    *os.File
	path string
	mem  strings.Builder
	traj *trajectory.Writer
}

func sanitizeTranscriptText(text string) string {
	if text == "" {
		return text
	}
	if !strings.ContainsRune(text, '\x00') {
		return text
	}
	return strings.ReplaceAll(text, "\x00", "")
}

func (t *Transcript) compactNULsIfNeeded() error {
	if t == nil {
		return nil
	}
	content := t.Content()
	if !strings.ContainsRune(content, '\x00') {
		return nil
	}
	cleaned := sanitizeTranscriptText(content)
	if err := t.rewrite(cleaned); err != nil {
		return fmt.Errorf("failed to compact transcript: %w", err)
	}
	return nil
}

func (t *Transcript) rewrite(cleaned string) error {
	if t == nil {
		return nil
	}
	if err := t.f.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(t.path, []byte(cleaned), 0o644); err != nil {
		return fmt.Errorf("failed to rewrite transcript: %w", err)
	}
	f, err := os.OpenFile(t.path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to reopen transcript: %w", err)
	}
	t.f = f
	t.mem.Reset()
	t.mem.WriteString(cleaned)
	return nil
}

func (t *Transcript) AppendRaw(text string) error {
	if t == nil {
		return nil
	}
	if text == "" {
		return nil
	}
	text = sanitizeTranscriptText(text)
	if text == "" {
		return nil
	}
	t.mem.WriteString(text)
	_, err := t.f.WriteString(text)
	t.emit("raw", map[string]any{
		"written_bytes": len(text),
	})
	return err
}

type PatchValidationMessage struct {
	Severity string
	Path     string
	Line     int
	Message  string
	Raw      string
}

type PatchValidationRecord struct {
	Operation    string
	Status       string
	PatchPath    string
	PatchPreview string
	PatchInput   string
	Stdout       string
	Stderr       string
	Error        string
	Messages     []PatchValidationMessage
	Conflicts    []string
}

func New(sessionID string) (*Transcript, error) {
	chatDir, err := artifacts.SessionChatDirectory(sessionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(chatDir, "agent-transcript.adoc")
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Transcript{f: f, path: path}, nil
}

// NewWithPath creates a transcript at an explicit path scoped to a session.
// If path is empty, it falls back to New(sessionID). Parent directories are
// created as needed.
func NewWithPath(path string, sessionID string) (*Transcript, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id required for transcript")
	}
	if strings.TrimSpace(path) == "" {
		return New(sessionID)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &Transcript{f: f, path: path}, nil
}

func (t *Transcript) Path() string { return t.path }

func (t *Transcript) Close() error {
	if t.f == nil {
		return nil
	}
	return t.f.Close()
}

// SetTrajectory attaches a trajectory writer used for structured transcript
// events. Passing nil disables emission.
func (t *Transcript) SetTrajectory(w *trajectory.Writer) {
	if t == nil {
		return
	}
	t.traj = w
}

func (t *Transcript) WriteHeader(goal string, sessionID string, _ any) error {
	formattedGoal := formatGoalSection(goal)
	s := fmt.Sprintf("= MCT-AGENT TRANSCRIPT\n\n== GOAL:\n\n%s\n\n", formattedGoal)
	s = sanitizeTranscriptText(s)
	if s == "" {
		return nil
	}
	t.mem.WriteString(s)
	_, err := t.f.WriteString(s)
	t.emit("header", map[string]any{
		"written_bytes": len(s),
		"goal_len":      len(formattedGoal),
	})
	return err
}

// formatGoalSection normalizes the GOAL header to highlight the task description
// and funnel the remaining prompt or carry-over context under a GOAL Context
// subheading.
func formatGoalSection(goal string) string {
	trimmed := strings.TrimSpace(goal)
	if trimmed == "" {
		return ""
	}

	paragraphs := splitGoalParagraphs(trimmed)
	description, detailsIdx := extractGoalDescription(paragraphs)
	descText := stripGoalEmphasis(description)
	if descText == "" && len(paragraphs) > 0 {
		descText = stripGoalEmphasis(paragraphs[0])
	}
	if descText == "" {
		return ""
	}

	contextBlocks := extractGoalContext(paragraphs, detailsIdx)
	if len(contextBlocks) == 0 {
		return fmt.Sprintf("%s", descText)
	}

	if detailsIdx < 0 && len(contextBlocks) == 1 && !strings.Contains(contextBlocks[0], "\n") {
		inline := normalizeInlineContext(contextBlocks[0])
		if inline == "" {
			return fmt.Sprintf("%s", descText)
		}
		return fmt.Sprintf("%s:%s", descText, inline)
	}

	context := strings.TrimSpace(strings.Join(contextBlocks, "\n\n"))
	if context == "" {
		return fmt.Sprintf("%s", descText)
	}

	return fmt.Sprintf("%s\n\n== PROBLEM:\n\n%s", descText, context)
}

func extractGoalDescription(paragraphs []string) (string, int) {
	for idx, block := range paragraphs {
		lower := strings.ToLower(strings.TrimSpace(block))
		if strings.HasPrefix(lower, "task details:") {
			detail := strings.TrimSpace(block[len("Task details:"):])
			if detail != "" {
				return detail, idx
			}
		}
	}
	return "", -1
}

func extractGoalContext(paragraphs []string, detailsIdx int) []string {
	if len(paragraphs) == 0 {
		return nil
	}
	start := detailsIdx + 1
	if detailsIdx < 0 {
		start = 1
	}
	if start >= len(paragraphs) {
		return nil
	}
	return paragraphs[start:]
}

func splitGoalParagraphs(goal string) []string {
	blocks := strings.Split(goal, "\n\n")
	out := make([]string, 0, len(blocks))
	for _, block := range blocks {
		trimmed := strings.TrimSpace(block)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func stripGoalEmphasis(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	trimmed = strings.Trim(trimmed, "*")
	return strings.TrimSpace(trimmed)
}

func normalizeInlineContext(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	return strings.Join(strings.Fields(trimmed), " ")
}

func (t *Transcript) WriteTurn(step int, question, savedPath string, retrieved []string, summary string, decision string) error {
	var b strings.Builder
	if step >= 0 {
		b.WriteString(fmt.Sprintf("\n== TURN %d\n\n", step))
	}
	b.WriteString(question + "\n\n")
	if savedPath != "" {
		b.WriteString("mct chat: " + savedPath + "\n\n")
	}
	if len(retrieved) > 0 {
		b.WriteString("Retrieved File Paths:\n")
		for _, p := range retrieved {
			b.WriteString("* " + p + "\n")
		}
		b.WriteString("\n")
	}
	if summary != "" {
		b.WriteString("=== ANSWER\n\n")
		b.WriteString(summary + "\n\n")
	}
	b.WriteString("Planner decision: ")
	b.WriteString(decision)
	b.WriteString("\n")
	s := sanitizeTranscriptText(b.String())
	if s == "" {
		return nil
	}
	if err := t.compactNULsIfNeeded(); err != nil {
		return err
	}
	t.mem.WriteString(s)
	_, err := t.f.WriteString(s)
	t.emit("turn", map[string]any{
		"step":            step,
		"written_bytes":   len(s),
		"retrieved_count": len(retrieved),
		"decision":        decision,
	})
	return err
}

func (t *Transcript) WriteFinal(answer string, step int, capped bool) error {
	var note string
	if capped {
		note = " (reached max-steps cap)"
	}
	s := fmt.Sprintf("\n== CONCLUSION%s (after %d turn(s))\n\n%s\n", note, step, answer)
	s = sanitizeTranscriptText(s)
	if s == "" {
		return nil
	}
	if err := t.compactNULsIfNeeded(); err != nil {
		return err
	}
	t.mem.WriteString(s)
	_, err := t.f.WriteString(s)
	t.emit("final", map[string]any{
		"step":          step,
		"written_bytes": len(s),
		"capped":        capped,
	})
	return err
}

func (t *Transcript) WritePatchValidation(step int, record PatchValidationRecord) error {
	if t == nil {
		return nil
	}

	var b strings.Builder
	b.WriteString("\n=== PATCH VALIDATION")
	if step > 0 {
		b.WriteString(fmt.Sprintf(" (Turn %d)", step))
	}
	b.WriteString("\n\n")
	if record.Operation != "" {
		b.WriteString("Operation: ")
		b.WriteString(record.Operation)
		b.WriteString("\n")
	}
	if record.Status != "" {
		b.WriteString("Status: ")
		b.WriteString(record.Status)
		b.WriteString("\n")
	}
	if record.PatchPath != "" {
		b.WriteString("Patch Path: ")
		b.WriteString(record.PatchPath)
		b.WriteString("\n")
	}
	if record.PatchPreview != "" {
		b.WriteString("Patch Preview:\n")
		b.WriteString(record.PatchPreview)
		b.WriteString("\n")
	}
	if record.PatchInput != "" {
		b.WriteString("Patch Input JSON:\n")
		b.WriteString(record.PatchInput)
		b.WriteString("\n")
	}
	if len(record.Messages) > 0 {
		b.WriteString("Messages:\n")
		for _, msg := range record.Messages {
			b.WriteString("* ")
			if msg.Severity != "" {
				b.WriteString("[")
				b.WriteString(msg.Severity)
				b.WriteString("] ")
			}
			if msg.Path != "" {
				b.WriteString(msg.Path)
				if msg.Line > 0 {
					b.WriteString(fmt.Sprintf(":%d", msg.Line))
				}
				b.WriteString(" - ")
			}
			if msg.Message != "" {
				b.WriteString(msg.Message)
			} else if msg.Raw != "" {
				b.WriteString(msg.Raw)
			}
			b.WriteString("\n")
		}
	}
	if len(record.Conflicts) > 0 {
		b.WriteString("Conflicts:\n")
		for _, conflict := range record.Conflicts {
			b.WriteString(conflict)
			if !strings.HasSuffix(conflict, "\n") {
				b.WriteString("\n")
			}
		}
	}
	if record.Stdout != "" {
		b.WriteString("Validation Stdout:\n")
		b.WriteString(record.Stdout)
		b.WriteString("\n")
	}
	if record.Stderr != "" {
		b.WriteString("Validation Stderr:\n")
		b.WriteString(record.Stderr)
		b.WriteString("\n")
	}
	if record.Error != "" {
		b.WriteString("Error:\n")
		b.WriteString(record.Error)
		b.WriteString("\n")
	}
	s := sanitizeTranscriptText(b.String())
	if s == "" {
		return nil
	}
	if err := t.compactNULsIfNeeded(); err != nil {
		return err
	}
	t.mem.WriteString(s)
	_, err := t.f.WriteString(s)
	t.emit("patch-validation", map[string]any{
		"written_bytes": len(s),
		"has_messages":  len(record.Messages) > 0,
		"operation":     record.Operation,
	})
	return err
}

// Tail returns up to the last maxBytes of the transcript file.
func (t *Transcript) Tail(maxBytes int) (string, error) {
	if t == nil {
		return "", nil
	}
	data := t.mem.String()
	if maxBytes <= 0 || len(data) <= maxBytes {
		return data, nil
	}
	return data[len(data)-maxBytes:], nil
}

// Content returns the full in-memory transcript content.
func (t *Transcript) Content() string {
	if t == nil {
		return ""
	}
	return t.mem.String()
}

// Restore seeds the transcript with existing content, typically used when resuming
// an interrupted session.
func (t *Transcript) Restore(content string) error {
	if t == nil {
		return nil
	}
	if t.f == nil {
		return fmt.Errorf("transcript file not initialised")
	}
	if content == "" {
		return nil
	}
	content = sanitizeTranscriptText(content)
	if content == "" {
		return nil
	}
	if _, err := t.f.WriteString(content); err != nil {
		return err
	}
	t.mem.Reset()
	if _, err := t.mem.WriteString(content); err != nil {
		return err
	}
	return nil
}

func (t *Transcript) emit(op string, payload map[string]any) {
	if t == nil || t.traj == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["event_version"] = 1
	payload["path"] = t.path
	payload["op"] = op
	evt := trajectory.Event{
		Kind:    "transcript.write",
		SpanID:  trajectory.NewSpanID(),
		Payload: payload,
	}
	if err := t.traj.Emit(context.Background(), evt); err != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] transcript emit error: %v\n", err)
	}
}

// DeduplicateFullDiffByFile removes prior full_diff turns referencing the given
// file, ensuring the transcript contains at most one DecisionFullDiff per file.
// This is best-effort: failures return errors but do not leave the transcript
// file handle in an invalid state.
func (t *Transcript) DeduplicateFullDiffByFile(filePath string) error {
	if t == nil {
		return nil
	}
	norm := filepath.ToSlash(strings.TrimSpace(filePath))
	if norm == "" {
		return nil
	}
	content := t.Content()
	if strings.TrimSpace(content) == "" {
		return nil
	}

	normalize := func(p string) string {
		rel := filepath.ToSlash(strings.TrimSpace(p))
		if rel == "" {
			return ""
		}
		return pathpkg.Clean(rel)
	}

	norm = normalize(norm)
	lines := strings.Split(content, "\n")
	prelude := make([]string, 0)
	turns := make([]struct {
		lines      []string
		isFullDiff bool
		files      map[string]struct{}
	}, 0)

	addFile := func(files map[string]struct{}, p string) map[string]struct{} {
		if p == "" {
			return files
		}
		if files == nil {
			files = map[string]struct{}{}
		}
		files[p] = struct{}{}
		return files
	}

	inTurn := false
	inRetrieved := false
	currentLines := []string{}
	currentFiles := map[string]struct{}{}
	currentIsFullDiff := false

	flushTurn := func() {
		if !inTurn {
			return
		}
		turns = append(turns, struct {
			lines      []string
			isFullDiff bool
			files      map[string]struct{}
		}{
			lines:      append([]string{}, currentLines...),
			isFullDiff: currentIsFullDiff,
			files:      currentFiles,
		})
		currentLines = []string{}
		currentFiles = map[string]struct{}{}
		currentIsFullDiff = false
		inTurn = false
		inRetrieved = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "== TURN ") {
			flushTurn()
			inTurn = true
			inRetrieved = false
			currentLines = []string{line}
			currentFiles = map[string]struct{}{}
			currentIsFullDiff = false
			continue
		}
		if !inTurn {
			prelude = append(prelude, line)
			continue
		}
		currentLines = append(currentLines, line)
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Planner decision:"):
			currentIsFullDiff = strings.TrimSpace(strings.TrimPrefix(trimmed, "Planner decision:")) == "full_diff"
		case trimmed == "Retrieved File Paths:":
			inRetrieved = true
		case inRetrieved && strings.HasPrefix(trimmed, "* "):
			currentFiles = addFile(currentFiles, normalize(strings.TrimPrefix(trimmed, "* ")))
		case inRetrieved && trimmed == "":
			inRetrieved = false
		case inRetrieved:
			inRetrieved = false
		case strings.HasPrefix(trimmed, "Automatic full diff post-patch for:"):
			currentFiles = addFile(currentFiles, normalize(strings.TrimPrefix(trimmed, "Automatic full diff post-patch for:")))
		}
	}
	flushTurn()

	lastMatch := -1
	for idx, turn := range turns {
		if !turn.isFullDiff {
			continue
		}
		if _, ok := turn.files[norm]; ok {
			lastMatch = idx
		}
	}

	newLines := make([]string, 0, len(lines))
	newLines = append(newLines, prelude...)
	for idx, turn := range turns {
		if turn.isFullDiff {
			if _, ok := turn.files[norm]; ok && idx != lastMatch {
				continue
			}
		}
		newLines = append(newLines, turn.lines...)
	}

	cleaned := strings.Join(newLines, "\n")
	cleaned = sanitizeTranscriptText(cleaned)
	if err := t.rewrite(cleaned); err != nil {
		return fmt.Errorf("failed to write deduplicated transcript: %w", err)
	}
	return nil
}

// DeduplicatePatchPlan removes all prior patch plan sections
// (== PATCH PLAN CREATED / == PATCH PLAN UPDATED), keeping only the latest one.
func (t *Transcript) DeduplicatePatchPlan() error {
	if t == nil {
		return nil
	}
	content := t.Content()
	if strings.TrimSpace(content) == "" {
		return nil
	}

	lines := strings.Split(content, "\n")

	// Find all patch plan section boundaries
	type sectionBounds struct {
		start int
		end   int
	}
	var sections []sectionBounds

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "== PATCH PLAN CREATED") || strings.HasPrefix(line, "== PATCH PLAN UPDATED") {
			start := i
			end := len(lines) - 1
			// Find end of this section (next == marker or EOF)
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "== ") {
					end = j - 1
					break
				}
			}
			sections = append(sections, sectionBounds{start: start, end: end})
		}
	}

	// Nothing to deduplicate if 0 or 1 sections
	if len(sections) <= 1 {
		return nil
	}

	// Mark lines to remove (all sections except the last)
	removeSet := make(map[int]struct{})
	for _, sec := range sections[:len(sections)-1] {
		for i := sec.start; i <= sec.end; i++ {
			removeSet[i] = struct{}{}
		}
	}

	// Build new content excluding removed lines
	newLines := make([]string, 0, len(lines))
	for idx, line := range lines {
		if _, skip := removeSet[idx]; skip {
			continue
		}
		newLines = append(newLines, line)
	}

	cleaned := strings.Join(newLines, "\n")
	cleaned = sanitizeTranscriptText(cleaned)
	if err := t.rewrite(cleaned); err != nil {
		return fmt.Errorf("failed to write deduplicated transcript: %w", err)
	}
	return nil
}

// WritePatchPlanCreated writes an initial patch plan to the transcript.
// It first removes any prior patch plan sections to keep only the latest.
func (t *Transcript) WritePatchPlanCreated(step int, planDetails string) error {
	if t == nil {
		return nil
	}
	// Remove prior patch plan sections before writing the new one
	if err := t.DeduplicatePatchPlan(); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("\n== PATCH PLAN CREATED ==\n")
	if step > 0 {
		b.WriteString(fmt.Sprintf("Step: %d\n", step))
	}
	b.WriteString(planDetails)
	if !strings.HasSuffix(planDetails, "\n") {
		b.WriteString("\n")
	}

	s := sanitizeTranscriptText(b.String())
	if s == "" {
		return nil
	}
	t.mem.WriteString(s)
	_, err := t.f.WriteString(s)
	t.emit("patch_plan_created", map[string]any{
		"step":          step,
		"written_bytes": len(s),
	})
	if err != nil {
		return err
	}
	return t.compactNULsIfNeeded()
}

// WritePatchPlanUpdated writes an updated patch plan to the transcript.
// It first removes any prior patch plan sections to keep only the latest.
func (t *Transcript) WritePatchPlanUpdated(step int, planDetails string) error {
	if t == nil {
		return nil
	}
	// Remove prior patch plan sections before writing the new one
	if err := t.DeduplicatePatchPlan(); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("\n== PATCH PLAN UPDATED ==\n")
	if step > 0 {
		b.WriteString(fmt.Sprintf("Step: %d\n", step))
	}
	b.WriteString(planDetails)
	if !strings.HasSuffix(planDetails, "\n") {
		b.WriteString("\n")
	}

	s := sanitizeTranscriptText(b.String())
	if s == "" {
		return nil
	}
	t.mem.WriteString(s)
	_, err := t.f.WriteString(s)
	t.emit("patch_plan_updated", map[string]any{
		"step":          step,
		"written_bytes": len(s),
	})
	if err != nil {
		return err
	}
	return t.compactNULsIfNeeded()
}
