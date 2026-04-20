package planner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestFinalizeProviderPayloadUsesSimplifiedVisibleTags(t *testing.T) {
	type chatMessage struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	type requestPayload struct {
		Model    string        `json:"model"`
		Messages []chatMessage `json:"messages"`
	}

	var captured requestPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("unmarshal request body: %v\nbody=%s", err, string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	client := NewClient(ClientConfig{Model: llm.ResolvedModel{BaseURL: server.URL, Model: "test-model"}})
	conv := conversation.New("sess-payload", "Fix the flaky planner test.")
	conv.AddMessage("assistant", "Do you want the safer fix?", map[string]any{"type": "user_input_request"})
	conv.AddMessage("user", "Use the safer fix.", map[string]any{"type": "user_input_response"})
	conv.AddMessage("assistant", "Inspect the planner retry flow and summarize the root cause.", map[string]any{"type": "work_request", "turn": 1})
	conv.AddMessage("assistant", "The root cause is stale retry state after a failed parse.", map[string]any{"type": "work_result", "turn": 1})
	conv.AddMessage("user", "Don't edit the router.", nil)

	resp, err := client.Finalize(context.Background(), conv, "stale goal")
	if err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("unexpected response %q", resp)
	}
	if captured.Model != "test-model" {
		t.Fatalf("unexpected model %q", captured.Model)
	}
	if len(captured.Messages) < 7 {
		t.Fatalf("expected at least 7 payload messages, got %d", len(captured.Messages))
	}

	got := []chatMessage{
		captured.Messages[1],
		captured.Messages[2],
		captured.Messages[3],
		captured.Messages[4],
		captured.Messages[5],
		captured.Messages[6],
	}
	want := []chatMessage{
		{Role: "user", Content: "Fix the flaky planner test."},
		{Role: "assistant", Content: "[user_input_request] Do you want the safer fix?"},
		{Role: "user", Content: "[user_input_response] Use the safer fix."},
		{Role: "assistant", Content: "[work_request] Inspect the planner retry flow and summarize the root cause."},
		{Role: "assistant", Content: "[work_result] The root cause is stale retry state after a failed parse."},
		{Role: "user", Content: "Don't edit the router."},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("payload message %d = %#v, want %#v", i+1, got[i], want[i])
		}
		if strings.Contains(got[i].Content, "[original_goal]") || strings.Contains(got[i].Content, "[goal_update]") {
			t.Fatalf("payload message %d still uses removed goal tags: %#v", i+1, got[i])
		}
	}
	last := captured.Messages[len(captured.Messages)-1]
	if last.Role != "user" || last.Content != "Write the final answer using the conversation above as the source of truth.\n\nProduce a clear, self-contained final response grounded in the prior turns. If any important gaps or uncertainty remain, call them out briefly." {
		t.Fatalf("unexpected finalize payload message %#v", last)
	}
	if strings.Contains(last.Content, "Goal:") || strings.Contains(last.Content, "Current Goal:") {
		t.Fatalf("finalize payload should omit goal anchors, got %q", last.Content)
	}
}
