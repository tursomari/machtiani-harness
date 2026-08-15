package transcript

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
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

// AppendBlock writes a pre-rendered transcript block after sanitizing and
// compacting existing NUL bytes.
func (t *Transcript) AppendBlock(block string) error {
	if t == nil {
		return nil
	}
	if block == "" {
		return nil
	}
	block = sanitizeTranscriptText(block)
	if block == "" {
		return nil
	}
	if err := t.compactNULsIfNeeded(); err != nil {
		return err
	}
	t.mem.WriteString(block)
	_, err := t.f.WriteString(block)
	t.emit("block", map[string]any{
		"written_bytes": len(block),
	})
	return err
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
	if step > 0 {
		b.WriteString(fmt.Sprintf("\n== TURN %d\n\n", step))
	} else if step == 0 {
		b.WriteString("\n== CONTEXT\n\n")
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
	if strings.TrimSpace(decision) != "" {
		b.WriteString("Planner decision: ")
		b.WriteString(decision)
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
	return t.rewrite(content)
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
