package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestReasoningCompatibilityLearnsSuccessfulShape(t *testing.T) {
	var mu sync.Mutex
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		payloads = append(payloads, payload)
		mu.Unlock()
		if _, ok := payload[reasoningFormatEffort]; ok {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unknown parameter: 'reasoning_effort'.","param":"reasoning_effort","code":"unknown_parameter"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	model := ResolvedModel{Alias: "demo", ProviderName: "custom", BaseURL: server.URL, Model: "demo-model"}
	payload := map[string]any{"reasoning": map[string]any{"effort": "low"}, "model": model.Model, "messages": []any{}}
	for call := 0; call < 2; call++ {
		got, _, err := executeWithReasoningCompatibility(context.Background(), model, payload, false, nil, llmAttemptMeta{})
		if err != nil || got != "ok" {
			t.Fatalf("call %d = %q, %v", call+1, got, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 3 {
		t.Fatalf("request count = %d, want 3", len(payloads))
	}
	if _, ok := payloads[0][reasoningFormatEffort]; !ok {
		t.Fatalf("first payload = %#v, want reasoning_effort", payloads[0])
	}
	for i := 1; i < len(payloads); i++ {
		reasoning, ok := payloads[i][reasoningFormatObject].(map[string]any)
		if !ok || reasoning["effort"] != "low" || len(reasoning) != 1 {
			t.Fatalf("payload %d reasoning = %#v", i+1, payloads[i][reasoningFormatObject])
		}
	}
}

func TestReasoningCompatibilityUsesExplicitObjectAsFinalFallback(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := payload[reasoningFormatEffort]; ok {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"unsupported reasoning_effort","param":"reasoning_effort"}}`)
			return
		}
		reasoning, _ := payload[reasoningFormatObject].(map[string]any)
		if len(reasoning) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"reasoning configuration requires enabled and budget_tokens","param":"reasoning"}}`)
			return
		}
		if reasoning["effort"] != "high" || reasoning["enabled"] != true {
			t.Fatalf("explicit reasoning = %#v", reasoning)
		}
		if value, exists := reasoning["budget_tokens"]; !exists || value != nil {
			t.Fatalf("budget_tokens was not preserved as JSON null: %#v", reasoning)
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"explicit"}}]}`)
	}))
	defer server.Close()

	model := ResolvedModel{Alias: "explicit", ProviderName: "custom", BaseURL: server.URL, Model: "demo-model"}
	payload := map[string]any{reasoningFormatEffort: "high", "model": model.Model, "messages": []any{}}
	got, _, err := executeWithReasoningCompatibility(context.Background(), model, payload, false, nil, llmAttemptMeta{})
	if err != nil || got != "explicit" {
		t.Fatalf("result = %q, %v", got, err)
	}
	if requests != 3 {
		t.Fatalf("request count = %d, want 3", requests)
	}
}

func TestReasoningCompatibilityDoesNotRetryUnrelatedBadRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.WriteHeader(http.StatusBadRequest)
		if _, ok := payload[reasoningFormatEffort]; ok {
			fmt.Fprint(w, `{"error":{"message":"Unknown parameter: 'reasoning_effort'.","param":"reasoning_effort","code":"unknown_parameter"}}`)
			return
		}
		fmt.Fprint(w, `{"error":{"message":"messages are invalid","param":"messages"}}`)
	}))
	defer server.Close()

	model := ResolvedModel{Alias: "unrelated", ProviderName: "custom", BaseURL: server.URL, Model: "demo-model"}
	payload := map[string]any{reasoningFormatEffort: "low", "model": model.Model, "messages": []any{}}
	_, _, err := executeWithReasoningCompatibility(context.Background(), model, payload, false, nil, llmAttemptMeta{})
	if err == nil || !strings.Contains(err.Error(), "messages are invalid") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "config model set") {
		t.Fatalf("unrelated error was replaced by reasoning guidance: %v", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
}

func TestOpenRouterPrefersReasoningObject(t *testing.T) {
	model := ResolvedModel{ProviderName: "openrouter", BaseURL: "https://openrouter.ai/api/v1", Model: "demo"}
	_, formats, ok := reasoningFormatsFor(model, map[string]any{reasoningFormatEffort: "low"})
	if !ok || len(formats) == 0 || formats[0] != reasoningFormatObject {
		t.Fatalf("formats = %#v, ok = %v", formats, ok)
	}
}

func TestParseParamsJSONPreservesNull(t *testing.T) {
	params, err := parseParamsJSON(`{"reasoning":{"effort":"high","budget_tokens":null,"enabled":true}}`)
	if err != nil {
		t.Fatal(err)
	}
	reasoning := params["reasoning"].(map[string]any)
	if value, exists := reasoning["budget_tokens"]; !exists || value != nil {
		t.Fatalf("reasoning = %#v", reasoning)
	}
}
