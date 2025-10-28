package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

func TestEmitLLMEventUsesLLMComponent(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "traj.jsonl")
	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("trajectory.New error: %v", err)
	}

	ctx := trajectory.ContextWithWriter(context.Background(), writer)
	emitLLMEvent(ctx, "error", "llm.test", map[string]any{"foo": "bar"}, context.DeadlineExceeded)

	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trajectory file: %v", err)
	}
	lines := bytesFields(data)
	if len(lines) != 1 {
		t.Fatalf("expected 1 event, got %d", len(lines))
	}
	var rec map[string]any
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got := rec["component"]; got != "llm" {
		t.Fatalf("expected component 'llm', got %v", got)
	}
}

func bytesFields(data []byte) [][]byte {
	rows := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	out := make([][]byte, 0, len(rows))
	for _, row := range rows {
		trimmed := bytes.TrimSpace(row)
		if len(trimmed) > 0 {
			out = append(out, trimmed)
		}
	}
	return out
}
