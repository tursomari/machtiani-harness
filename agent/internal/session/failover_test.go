package session

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
)

func TestFailoverLogTrackerSuppressesDuplicates(t *testing.T) {
	tracker := newFailoverLogTracker()
	sessionID := "session-1"
	fromModel := map[string]any{
		"alias":    "primary",
		"model":    "primary-model",
		"base_url": "https://api.example.com",
		"endpoint": "/chat/completions",
	}
	toModel := map[string]any{
		"alias":    "fallback-1",
		"model":    "fallback-model",
		"base_url": "https://api.example.com",
		"endpoint": "/chat/completions",
	}
	startEvent := listener.Event{
		Event: trajectory.Event{
			Kind:         "llm.failover.start",
			ParentSpanID: "turn-1",
			Payload: map[string]any{
				"alias":  "primary",
				"source": "planner",
				"from":   fromModel,
				"to":     toModel,
			},
		},
		SessionID: sessionID,
	}

	msg := tracker.process(startEvent)
	if msg == "" {
		t.Fatalf("expected start message")
	}
	if duplicate := tracker.process(startEvent); duplicate != "" {
		t.Fatalf("expected duplicate start message to be suppressed, got %q", duplicate)
	}

	resultEvent := listener.Event{
		Event: trajectory.Event{
			Kind:         "llm.failover.result",
			ParentSpanID: "turn-1",
			Payload: map[string]any{
				"alias":   "primary",
				"source":  "planner",
				"from":    fromModel,
				"to":      toModel,
				"success": true,
			},
		},
		SessionID: sessionID,
	}

	msg = tracker.process(resultEvent)
	if msg == "" {
		t.Fatalf("expected result message")
	}
	if duplicate := tracker.process(resultEvent); duplicate != "" {
		t.Fatalf("expected duplicate result message to be suppressed, got %q", duplicate)
	}

	endEvent := listener.Event{
		Event: trajectory.Event{
			Kind: "llm.request.end",
			Payload: map[string]any{
				"alias": "primary",
				"model": map[string]any{
					"alias":    "primary",
					"model":    "primary-model",
					"base_url": "https://api.example.com",
					"endpoint": "/chat/completions",
				},
			},
		},
		SessionID: sessionID,
	}

	msg = tracker.process(endEvent)
	if msg == "" {
		t.Fatalf("expected recovery message")
	}
}

func TestStartLLMFailoverLoggerDiagWriterCapturesListenerError(t *testing.T) {
	// Use a non-existent trajectory file path so Subscribe fails immediately.
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.jsonl")

	var diagBuf bytes.Buffer
	cancel, done, err := startLLMFailoverLogger(nil, missingPath, &diagBuf)
	if err != nil {
		t.Fatalf("startLLMFailoverLogger: %v", err)
	}
	defer cancel()

	// Wait for the goroutine to exit.
	<-done

	output := diagBuf.String()
	if !strings.Contains(output, "[trajectory] failover listener stopped") {
		t.Fatalf("expected listener stopped message in diagWriter, got: %s", output)
	}
}
