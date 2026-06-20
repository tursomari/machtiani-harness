package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

func TestOfflineLLMProducesTrajectoryEvidence(t *testing.T) {
	sessionID := "test-session-offline"
	path := filepath.Join(t.TempDir(), "agent.jsonl")
	writer, err := trajectory.New(trajectory.Config{SessionID: sessionID, Path: path, Component: "agent", ExcerptLen: 128})
	if err != nil {
		t.Fatalf("trajectory: %v", err)
	}
	cfg := legacyConfig{trajectoryExcerpt: 128}
	sess := newSessionTelemetry(writer, sessionID, "offline goal", cfg, "", BuildInfo{}, os.Stderr)
	if sess == nil {
		t.Fatalf("expected session telemetry")
	}
	turn := sess.StartTurn(1, 1)
	if turn == nil {
		t.Fatalf("expected turn telemetry")
	}
	ctx := attachTrajectory(context.Background(), writer, turn.span.ID)
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	model := llm.ResolvedModel{
		Alias:   "offline-primary",
		BaseURL: "http://127.0.0.1:65535",
		APIKey:  "test",
		Model:   "test-model",
	}
	_, chatErr := llm.ChatWithResolved(ctx, model, nil, []llm.Message{{Role: "user", Content: "hello"}})
	if chatErr == nil {
		t.Fatalf("expected offline error")
	}

	sess.EndTurn(turn, "initial", "error", nil, chatErr)
	sess.Finish("error", 1, chatErr)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trajectory: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	kinds := make(map[string]bool)
	networkError := false
	retrySeen := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec struct {
			Kind string `json:"kind"`
			Err  *struct {
				Category string `json:"category"`
				Code     string `json:"code"`
			} `json:"err,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		kinds[rec.Kind] = true
		if rec.Kind == "llm.request.error" && rec.Err != nil && rec.Err.Category == "network" {
			networkError = true
		}
		if rec.Kind == "llm.retry" {
			retrySeen = true
		}
	}

	expectedKinds := []string{
		"agent.session.start",
		"agent.turn.start",
		"llm.request.start",
		"llm.request.error",
		"llm.retry",
		"agent.turn.end",
		"agent.session.end",
	}
	for _, kind := range expectedKinds {
		if !kinds[kind] {
			t.Fatalf("missing trajectory event kind %q", kind)
		}
	}
	if !networkError {
		t.Fatalf("expected llm.request.error with network category")
	}
	if !retrySeen {
		t.Fatalf("expected llm.retry event")
	}
}
