package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type failingReader struct {
	data  []byte
	reads int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.reads == 0 {
		copy(p, r.data)
		r.reads++
		return len(r.data), nil
	}
	return 0, io.ErrUnexpectedEOF
}

func TestTryStreamThenFallbackEmitsSuffix(t *testing.T) {
	originalStreamClient := streamingHTTPClient
	originalTransport := http.DefaultClient.Transport
	streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		payload := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(&failingReader{data: []byte(payload)}),
			Request:    req,
		}, nil
	})}
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"choices":[{"message":{"content":"hello world"}}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})
	defer func() {
		streamingHTTPClient = originalStreamClient
		http.DefaultClient.Transport = originalTransport
	}()

	basePayload := map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}
	streamBody, err := encodePayload(basePayload, true)
	if err != nil {
		t.Fatalf("encode stream payload: %v", err)
	}
	nonStreamBody, err := encodePayload(basePayload, false)
	if err != nil {
		t.Fatalf("encode non-stream payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}

	var tokens []string
	result, err := tryStreamThenFallback(context.Background(), model, streamBody, nonStreamBody, func(tok string) {
		tokens = append(tokens, tok)
	})
	if err != nil {
		t.Fatalf("tryStreamThenFallback error: %v", err)
	}
	if result != "hello world" {
		t.Fatalf("unexpected result: %q", result)
	}
	expected := []string{"hello", " world"}
	if len(tokens) != len(expected) {
		t.Fatalf("unexpected token count: got %d want %d", len(tokens), len(expected))
	}
	for i := range expected {
		if tokens[i] != expected[i] {
			t.Fatalf("token %d mismatch: got %q want %q", i, tokens[i], expected[i])
		}
	}
}

func TestRetryDelayHonorsRetryAfter(t *testing.T) {
	header := make(http.Header)
	header.Set("Retry-After", "5")
	err := &HTTPResponseError{Status: http.StatusTooManyRequests, Header: header}
	delay := retryDelay(err, time.Second)
	if delay != 5*time.Second {
		t.Fatalf("unexpected retry delay: got %v want %v", delay, 5*time.Second)
	}
}

func TestRetryDelayCapsRetryAfter(t *testing.T) {
	header := make(http.Header)
	header.Set("Retry-After", "120")
	err := &HTTPResponseError{Status: http.StatusServiceUnavailable, Header: header}
	delay := retryDelay(err, time.Second)
	if delay != retryAfterCap {
		t.Fatalf("unexpected capped delay: got %v want %v", delay, retryAfterCap)
	}
}

func TestChatWithResolvedFallback_SwitchesOnHTTP400(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	var seen []string
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		seen = append(seen, payload.Model)
		switch payload.Model {
		case "bad-model":
			resp := &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"not valid"}}`)),
				Request:    req,
			}
			return resp, nil
		case "good-model":
			resp := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"fallback ok"}}]}`)),
				Request:    req,
			}
			return resp, nil
		default:
			return nil, nil
		}
	})

	primary := ResolvedModel{Alias: "bad", BaseURL: "http://example.com", Endpoint: "/chat/completions", Model: "bad-model"}
	fallback := ResolvedModel{Alias: "good", BaseURL: "http://example.com", Endpoint: "/chat/completions", Model: "good-model"}
	messages := []Message{{Role: "user", Content: "hi"}}

	got, err := ChatWithResolvedFallback(context.Background(), primary, nil, []ResolvedModel{fallback}, nil, messages)
	if err != nil {
		t.Fatalf("ChatWithResolvedFallback error: %v", err)
	}
	if got != "fallback ok" {
		t.Fatalf("unexpected response: %q", got)
	}
	if len(seen) != 2 || seen[0] != "bad-model" || seen[1] != "good-model" {
		t.Fatalf("unexpected request order: %v", seen)
	}
}

func TestChatRespectsAPIKeyOverridesFromContext(t *testing.T) {
	ResetConfigForTesting()
	t.Cleanup(ResetConfigForTesting)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	config := []byte(`default_model = "orch"

[providers.openai]
base_url = "https://stub.example"
api_key = ""

[models.orch]
provider = "openai"
model = "gpt-4"
`)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)
	t.Setenv("MCT_LLM_TEST_STUB", "context-override")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ORCH_API_KEY", "")

	msgs := []Message{{Role: "user", Content: "Hello"}}
	if _, err := Chat(context.Background(), "orch", nil, msgs); err == nil {
		t.Fatalf("expected error without API key override")
	}

	overrides := map[string]string{"openai": "cli-key"}
	ctx := WithAPIKeyOverrides(context.Background(), overrides)
	resp, err := Chat(ctx, "orch", nil, msgs)
	if err != nil {
		t.Fatalf("Chat with overrides returned error: %v", err)
	}
	if resp == "" {
		t.Fatalf("expected stub response, got empty string")
	}
}
