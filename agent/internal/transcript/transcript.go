package transcript

import (
	"context"
	"fmt"
	"os"
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
		return fmt.Sprintf("***%s***", descText)
	}

	if detailsIdx < 0 && len(contextBlocks) == 1 && !strings.Contains(contextBlocks[0], "\n") {
		inline := normalizeInlineContext(contextBlocks[0])
		if inline == "" {
			return fmt.Sprintf("***%s***", descText)
		}
		return fmt.Sprintf("***%s:%s***", descText, inline)
	}

	context := strings.TrimSpace(strings.Join(contextBlocks, "\n\n"))
	if context == "" {
		return fmt.Sprintf("***%s***", descText)
	}

	return fmt.Sprintf("***%s***\n\n== PROBLEM:\n\n%s", descText, context)
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
	b.WriteString(fmt.Sprintf("\n== TURN %d\n\n", step))
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
	s := b.String()
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
	s := b.String()
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
