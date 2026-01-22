package transcript

import (
	"context"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
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

func (t *Transcript) WriteHeader(originalPrompt string, taskDescription string, sessionID string, _ any) error {
	formattedGoal := formatGoalSection(originalPrompt, taskDescription)
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

// formatGoalSection renders the transcript GOAL section with the original
// prompt verbatim, optionally appending a meta task description block.
func formatGoalSection(originalPrompt string, taskDescription string) string {
	if strings.TrimSpace(originalPrompt) == "" {
		return ""
	}
	trimmedTask := strings.TrimSpace(taskDescription)
	if trimmedTask == "" {
		return originalPrompt
	}
	return fmt.Sprintf("%s\n\n---\n%s\n---", originalPrompt, trimmedTask)
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

// ExtractFullDiffs returns the formatted full diffs for all patched files
// from the transcript, in the order they appear.
func (t *Transcript) ExtractFullDiffs() (string, error) {
	if t == nil {
		return "", nil
	}
	return ExtractFullDiffsFromContent(t.Content())
}

// ExtractFullDiffsFromContent parses the transcript content for full_diff turns
// and returns the concatenated diff summaries in order.
func ExtractFullDiffsFromContent(content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", nil
	}

	normalize := func(p string) string {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			return ""
		}
		cleaned := pathpkg.Clean(filepath.ToSlash(trimmed))
		if cleaned == "." {
			return ""
		}
		return cleaned
	}

	extractFile := func(line string) string {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		lower := strings.ToLower(trimmed)
		const unifiedPrefix = "automatic unified diff post-patch for:"
		const fullPrefix = "automatic full diff post-patch for:"
		switch {
		case strings.HasPrefix(lower, unifiedPrefix):
			return normalize(strings.TrimSpace(trimmed[len(unifiedPrefix):]))
		case strings.HasPrefix(lower, fullPrefix):
			return normalize(strings.TrimSpace(trimmed[len(fullPrefix):]))
		}
		const unifiedHeaderPrefix = "=== UNIFIED DIFF OF PATCHED FILE:"
		const fullHeaderPrefix = "=== FULL DIFF OF PATCHED FILE:"
		switch {
		case strings.HasPrefix(upper, unifiedHeaderPrefix):
			rest := strings.TrimSpace(trimmed[len(unifiedHeaderPrefix):])
			rest = strings.TrimSpace(strings.TrimSuffix(rest, "==="))
			return normalize(rest)
		case strings.HasPrefix(upper, fullHeaderPrefix):
			rest := strings.TrimSpace(trimmed[len(fullHeaderPrefix):])
			rest = strings.TrimSpace(strings.TrimSuffix(rest, "==="))
			return normalize(rest)
		}
		return ""
	}

	trimAnswer := func(lines []string) string {
		start := 0
		for start < len(lines) && strings.TrimSpace(lines[start]) == "" {
			start++
		}
		end := len(lines)
		for end > start && strings.TrimSpace(lines[end-1]) == "" {
			end--
		}
		if start >= end {
			return ""
		}
		return strings.Join(lines[start:end], "\n")
	}

	lines := strings.Split(content, "\n")
	decisions := make([]struct {
		file    string
		content string
		order   int
	}, 0)
	indices := map[string]int{}
	order := 0

	var (
		inTurn      bool
		inAnswer    bool
		inRetrieved bool
		decision    string
		answerLines []string
		turnFile    string
	)

	resetTurn := func() {
		inTurn = false
		inAnswer = false
		inRetrieved = false
		decision = ""
		answerLines = nil
		turnFile = ""
	}

	flushTurn := func() {
		if !inTurn {
			return
		}
		decisionValue := strings.ToLower(strings.TrimSpace(decision))
		if decisionValue == "full_diff" {
			summary := trimAnswer(answerLines)
			if summary != "" {
				order++
				fileKey := normalize(turnFile)
				if fileKey == "" {
					decisions = append(decisions, struct {
						file    string
						content string
						order   int
					}{file: "", content: summary, order: order})
				} else if idx, ok := indices[fileKey]; ok {
					decisions[idx].content = summary
					decisions[idx].order = order
				} else {
					indices[fileKey] = len(decisions)
					decisions = append(decisions, struct {
						file    string
						content string
						order   int
					}{file: fileKey, content: summary, order: order})
				}
			}
		}
		resetTurn()
	}

	isTurnHeader := func(line string) bool {
		trimmed := strings.TrimSpace(line)
		return strings.HasPrefix(strings.ToUpper(trimmed), "== TURN ")
	}

	for _, line := range lines {
		if isTurnHeader(line) {
			flushTurn()
			inTurn = true
			continue
		}
		if !inTurn {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "=== ANSWER") {
			inAnswer = true
			continue
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "planner decision:") {
			decision = strings.TrimSpace(trimmed[len("planner decision:"):])
			inAnswer = false
			continue
		}
		if strings.EqualFold(trimmed, "Retrieved File Paths:") {
			inRetrieved = true
			continue
		}
		if inRetrieved {
			if strings.HasPrefix(strings.TrimSpace(trimmed), "*") {
				file := strings.TrimSpace(strings.TrimPrefix(trimmed, "*"))
				if normalized := normalize(file); normalized != "" && turnFile == "" {
					turnFile = normalized
				}
				continue
			}
			if trimmed == "" {
				inRetrieved = false
				continue
			}
			inRetrieved = false
		}
		if candidate := extractFile(line); candidate != "" {
			turnFile = candidate
		}
		if inAnswer {
			answerLines = append(answerLines, line)
		}
	}
	flushTurn()

	if len(decisions) == 0 {
		return "", nil
	}
	sort.Slice(decisions, func(i, j int) bool {
		return decisions[i].order < decisions[j].order
	})
	fullDiffs := make([]string, 0, len(decisions))
	for _, item := range decisions {
		if strings.TrimSpace(item.content) == "" {
			continue
		}
		fullDiffs = append(fullDiffs, item.content)
	}
	if len(fullDiffs) == 0 {
		return "", nil
	}
	return strings.Join(fullDiffs, "\n\n"), nil
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
		case strings.HasPrefix(trimmed, "Automatic unified diff post-patch for:"):
			currentFiles = addFile(currentFiles, normalize(strings.TrimPrefix(trimmed, "Automatic unified diff post-patch for:")))
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

const patchPlanDedupKeep = 2

// DeduplicatePatchPlan removes older patch plan sections
// (== PATCH PLAN CREATED / == PATCH PLAN UPDATED), keeping the most recent ones.
// The retention count is controlled by patchPlanDedupKeep.
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

	// Nothing to deduplicate if within the retention limit.
	if patchPlanDedupKeep <= 0 || len(sections) <= patchPlanDedupKeep {
		return nil
	}

	// Mark lines to remove (all sections except the most recent N)
	removeSet := make(map[int]struct{})
	for _, sec := range sections[:len(sections)-patchPlanDedupKeep] {
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
// It then removes older patch plan sections to keep recent history.
func (t *Transcript) WritePatchPlanCreated(step int, planDetails string) error {
	if t == nil {
		return nil
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
	if err := t.DeduplicatePatchPlan(); err != nil {
		fmt.Fprintf(os.Stderr, "[patch-plan] deduplicate failed: %v\n", err)
	}
	return t.compactNULsIfNeeded()
}

// WritePatchPlanUpdated writes an updated patch plan to the transcript.
// It then removes older patch plan sections to keep recent history.
func (t *Transcript) WritePatchPlanUpdated(step int, planDetails string) error {
	if t == nil {
		return nil
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
	if err := t.DeduplicatePatchPlan(); err != nil {
		fmt.Fprintf(os.Stderr, "[patch-plan] deduplicate failed: %v\n", err)
	}
	return t.compactNULsIfNeeded()
}
