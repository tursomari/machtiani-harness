package session

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
)

func TestFormatLLMRetryEvent(t *testing.T) {
	evt := listener.Event{
		Event: trajectory.Event{
			Kind: "llm.retry",
			Payload: map[string]any{
				"model": map[string]any{
					"alias": "primary",
					"model": "gpt-4.1",
				},
				"mode":         "non-stream",
				"attempt":      3,
				"next_attempt": 4,
				"wait_ms":      2000,
				"status":       429,
			},
			Err: &trajectory.ErrorInfo{Message: "llm http error 429: rate limited"},
		},
	}

	msg := formatLLMRetryEvent(evt)
	for _, want := range []string{"[llm retry]", "primary gpt-4.1", "non-stream", "attempt 3 failed", "retrying attempt 4", "2s", "status 429"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected %q in %q", want, msg)
		}
	}
}
