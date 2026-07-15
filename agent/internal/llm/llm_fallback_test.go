package llm

import (
	"context"
	"encoding/json"
	"errors"
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

func TestTryStreamThenFallbackReturnsContextOverflowWithoutNonStreamRetry(t *testing.T) {
	originalStreamClient := streamingHTTPClient
	originalTransport := http.DefaultClient.Transport
	defer func() {
		streamingHTTPClient = originalStreamClient
		http.DefaultClient.Transport = originalTransport
	}()

	streamAttempts := 0
	streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		streamAttempts++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"context_length_exceeded"}}`)),
			Request:    req,
		}, nil
	})}
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected non-stream retry: %s", req.URL.String())
		return nil, nil
	})

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
	_, err = tryStreamThenFallback(context.Background(), model, streamBody, nonStreamBody, func(string) {})
	if !IsContextOverflow(err) {
		t.Fatalf("error = %v, want structured context overflow", err)
	}
	if streamAttempts != 1 {
		t.Fatalf("stream attempts = %d, want 1", streamAttempts)
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

func TestNonStreamRetryBackoffIsExponential(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 1, want: 1 * time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 3, want: 4 * time.Second},
		{attempt: 4, want: 8 * time.Second},
		{attempt: 5, want: 16 * time.Second},
		{attempt: 6, want: nonStreamRetryMaxBackoff},
		{attempt: 8, want: nonStreamRetryMaxBackoff},
	}
	for _, tt := range tests {
		if got := nonStreamRetryBackoff(tt.attempt); got != tt.want {
			t.Fatalf("attempt %d: got %v want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestTryStreamThenFallbackRetriesStreamAfterHTTP429(t *testing.T) {
	originalStreamClient := streamingHTTPClient
	originalTransport := http.DefaultClient.Transport
	defer func() {
		streamingHTTPClient = originalStreamClient
		http.DefaultClient.Transport = originalTransport
	}()

	streamAttempts := 0
	probeAttempts := 0
	streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		streamAttempts++
		if streamAttempts < 3 {
			header := make(http.Header)
			header.Set("Retry-After", "0")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"stream rate limited"}}`)),
				Request:    req,
			}, nil
		}
		body := strings.Join([]string{
			`data: {"choices":[{"delta":{"content":"done"}}]}`,
			"",
			"data: [DONE]",
			"",
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages []Message `json:"messages"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeAttempts++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		t.Fatalf("unexpected non-stream fallback request: %s", req.URL.String())
		return nil, nil
	})

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
	if result != "done" {
		t.Fatalf("unexpected result: %q", result)
	}
	if streamAttempts != 3 {
		t.Fatalf("unexpected stream attempts: got %d want 3", streamAttempts)
	}
	if probeAttempts != 2 {
		t.Fatalf("unexpected probe attempts: got %d want 2", probeAttempts)
	}
	if len(tokens) != 1 || tokens[0] != "done" {
		t.Fatalf("unexpected streamed tokens: %v", tokens)
	}
}

// TestTryStreamThenFallbackStopsRetryingStreamOnContextCancel verifies that
// context cancellation during stream retries stops the process without
// reaching the non-stream fallback.
func TestTryStreamThenFallbackStopsRetryingStreamOnContextCancel(t *testing.T) {
	originalStreamClient := streamingHTTPClient
	originalTransport := http.DefaultClient.Transport
	defer func() {
		streamingHTTPClient = originalStreamClient
		http.DefaultClient.Transport = originalTransport
	}()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	streamAttempts := 0
	nonStreamAttempts := 0
	streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		streamAttempts++
		if streamAttempts == 4 {
			cancel()
		}
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`)),
			Request:    req,
		}, nil
	})}
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := payload.MaxTokens == 1 && len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		nonStreamAttempts++
		t.Fatalf("unexpected non-stream fallback request: %s", req.URL.String())
		return nil, nil
	})

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
	_, err = tryStreamThenFallback(ctx, model, streamBody, nonStreamBody, func(string) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if streamAttempts < 4 {
		t.Fatalf("expected repeated stream retries before cancellation, got %d attempts", streamAttempts)
	}
	if nonStreamAttempts != 0 {
		t.Fatalf("unexpected non-stream attempts: got %d want 0", nonStreamAttempts)
	}
}

func TestExecuteOnceWithRetriesRetriesPastLegacyLimit(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	fullAttempts := 0
	probeAttempts := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := payload.MaxTokens == 1 && len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeAttempts++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fullAttempts++
		if fullAttempts < 6 {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"temporarily unavailable"}}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"finally ok"}}]}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	got, err := executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err != nil {
		t.Fatalf("executeOnceWithRetries error: %v", err)
	}
	if got != "finally ok" {
		t.Fatalf("unexpected result: %q", got)
	}
	if fullAttempts != 6 {
		t.Fatalf("unexpected full attempts: got %d want 6", fullAttempts)
	}
	if probeAttempts != 5 {
		t.Fatalf("unexpected probe attempts: got %d want 5", probeAttempts)
	}
}

func TestExecuteOnceWithRetriesStopsOnContextCancel(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	attempts := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 6 {
			cancel()
		}
		header := make(http.Header)
		header.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"keep retrying"}}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	_, err = executeOnceWithRetries(ctx, model, body, llmAttemptMeta{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if attempts < 6 {
		t.Fatalf("expected repeated retries before cancellation, got %d attempts", attempts)
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

func TestExecuteOnceWithRetriesStopsAfterMaxRetries(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	fullAttempts := 0
	probeAttempts := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := payload.MaxTokens == 1 && len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeAttempts++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fullAttempts++
		header := make(http.Header)
		header.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	_, err = executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err == nil {
		t.Fatalf("expected error after max retries, got nil")
	}
	if fullAttempts != maxRetries {
		t.Fatalf("expected %d full attempts, got %d", maxRetries, fullAttempts)
	}
	if probeAttempts != maxRetries-1 {
		t.Fatalf("expected %d probe attempts, got %d", maxRetries-1, probeAttempts)
	}
	if !strings.Contains(err.Error(), "retry limit exhausted") {
		t.Fatalf("expected retry limit exhausted error, got: %v", err)
	}
}

func TestExecuteOnceWithRetriesSucceedsBeforeMaxRetries(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	fullAttempts := 0
	probeAttempts := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages []Message `json:"messages"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeAttempts++
			if probeAttempts < 2 {
				header := make(http.Header)
				header.Set("Retry-After", "0")
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     header,
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"probe rate limited"}}`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fullAttempts++
		if fullAttempts == 1 {
			header := make(http.Header)
			header.Set("Retry-After", "0")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"success"}}]}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	got, err := executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err != nil {
		t.Fatalf("executeOnceWithRetries error: %v", err)
	}
	if got != "success" {
		t.Fatalf("unexpected result: %q", got)
	}
	if fullAttempts != 2 {
		t.Fatalf("expected 2 full attempts, got %d", fullAttempts)
	}
	if probeAttempts != 2 {
		t.Fatalf("expected 2 probe attempts, got %d", probeAttempts)
	}
}

// TestTryStreamThenFallbackStopsAfterMaxRetries verifies that after the stream
// retry loop exhausts maxRetries, the non-stream fallback is attempted.
func TestTryStreamThenFallbackStopsAfterMaxRetries(t *testing.T) {
	originalStreamClient := streamingHTTPClient
	originalTransport := http.DefaultClient.Transport
	defer func() {
		streamingHTTPClient = originalStreamClient
		http.DefaultClient.Transport = originalTransport
	}()

	streamAttempts := 0
	probeAttempts := 0
	fallbackAttempts := 0
	streamBody, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, true)
	if err != nil {
		t.Fatalf("encode stream payload: %v", err)
	}
	nonStreamBody, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("encode non-stream payload: %v", err)
	}

	// Use 503 (retryable, non-429) so the stream loop triggers the probe path,
	// probes succeed, and full stream requests keep retrying until maxRetries.
	streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		streamAttempts++
		header := make(http.Header)
		header.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`)),
			Request:    req,
		}, nil
	})}
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := payload.MaxTokens == 1 && len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeAttempts++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fallbackAttempts++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"fallback success"}}]}`)),
			Request:    req,
		}, nil
	})

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	result, err := tryStreamThenFallback(context.Background(), model, streamBody, nonStreamBody, func(string) {})
	if err != nil {
		t.Fatalf("expected success via fallback, got error: %v", err)
	}
	if streamAttempts != maxRetries {
		t.Fatalf("expected %d stream attempts, got %d", maxRetries, streamAttempts)
	}
	if probeAttempts != maxRetries-1 {
		t.Fatalf("expected %d probe attempts, got %d", maxRetries-1, probeAttempts)
	}
	if fallbackAttempts != 1 {
		t.Fatalf("expected 1 fallback attempt, got %d", fallbackAttempts)
	}
	if result != "fallback success" {
		t.Fatalf("expected 'fallback success', got %q", result)
	}
}

// TestTryStreamThenFallbackFallbackFailsAfterMaxRetries verifies that when
// stream retries exhaust maxRetries and the non-stream fallback also fails,
// the fallback error is returned.
func TestTryStreamThenFallbackFallbackFailsAfterMaxRetries(t *testing.T) {
	originalStreamClient := streamingHTTPClient
	originalTransport := http.DefaultClient.Transport
	defer func() {
		streamingHTTPClient = originalStreamClient
		http.DefaultClient.Transport = originalTransport
	}()

	streamAttempts := 0
	probeAttempts := 0
	fallbackAttempts := 0
	streamBody, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, true)
	if err != nil {
		t.Fatalf("encode stream payload: %v", err)
	}
	nonStreamBody, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("encode non-stream payload: %v", err)
	}

	streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		streamAttempts++
		header := make(http.Header)
		header.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"unavailable"}}`)),
			Request:    req,
		}, nil
	})}
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := payload.MaxTokens == 1 && len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeAttempts++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fallbackAttempts++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"fallback failed"}}`)),
			Request:    req,
		}, nil
	})

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	_, err = tryStreamThenFallback(context.Background(), model, streamBody, nonStreamBody, func(string) {})
	if err == nil {
		t.Fatal("expected error when fallback also fails")
	}
	if streamAttempts != maxRetries {
		t.Fatalf("expected %d stream attempts, got %d", maxRetries, streamAttempts)
	}
	if probeAttempts != maxRetries-1 {
		t.Fatalf("expected %d probe attempts, got %d", maxRetries-1, probeAttempts)
	}
	if fallbackAttempts != 1 {
		t.Fatalf("expected 1 fallback attempt, got %d", fallbackAttempts)
	}
	if !strings.Contains(err.Error(), "fallback failed") {
		t.Fatalf("expected fallback error in message, got: %v", err)
	}
}

// TestExecuteOnceWithRetriesMultiple429sWithProbes verifies that each full
// request getting 429 triggers a probe cycle, but the full-request attempt
// counter (not the probe counter) is what maxRetries bounds.
func TestExecuteOnceWithRetriesMultiple429sWithProbes(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	fullReqCount := 0
	probeReqCount := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := payload.MaxTokens == 1 && len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeReqCount++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fullReqCount++
		header := make(http.Header)
		header.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hello"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	_, err = executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	// maxRetries bounds full requests. On attempts 1-9, a 429 triggers a probe.
	// On attempt 10, the maxRetries check fires first (before the 429 probe),
	// so no probe is sent on the final attempt.
	if fullReqCount != maxRetries {
		t.Fatalf("expected %d full requests, got %d", maxRetries, fullReqCount)
	}
	if probeReqCount != maxRetries-1 {
		t.Fatalf("expected %d probe requests, got %d", maxRetries-1, probeReqCount)
	}
	if !strings.Contains(err.Error(), "retry limit exhausted") {
		t.Fatalf("expected retry-exhausted error, got: %v", err)
	}
}

func TestExecuteOnceWithRetriesDoesNotRetryOnNonRetryableError(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	attempts := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"bad request"}}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	_, err = executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err == nil {
		t.Fatalf("expected error on non-retryable status, got nil")
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt for non-retryable error, got %d", attempts)
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

func TestExecuteOnceWithRetriesProbesOn429ThenSucceeds(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	fullAttempts := 0
	probeAttempts := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages []Message `json:"messages"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeAttempts++
			if probeAttempts < 2 {
				header := make(http.Header)
				header.Set("Retry-After", "0")
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Header:     header,
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"probe rate limited"}}`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fullAttempts++
		if fullAttempts == 1 {
			header := make(http.Header)
			header.Set("Retry-After", "0")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"success"}}]}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hi"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	got, err := executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err != nil {
		t.Fatalf("executeOnceWithRetries error: %v", err)
	}
	if got != "success" {
		t.Fatalf("unexpected result: %q", got)
	}
	if fullAttempts != 2 {
		t.Fatalf("expected 2 full attempts (initial + post-probe), got %d", fullAttempts)
	}
	if probeAttempts != 2 {
		t.Fatalf("expected 2 probe attempts, got %d", probeAttempts)
	}
}

func TestExecuteOnceWithRetriesProbeUsesMinimalBody(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	var probeBody []byte
	attempts := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages []Message `json:"messages"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeBody = body
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		attempts++
		if attempts == 1 {
			header := make(http.Header)
			header.Set("Retry-After", "0")
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"success"}}]}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "a much longer message that would be expensive to send repeatedly"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	_, err = executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err != nil {
		t.Fatalf("executeOnceWithRetries error: %v", err)
	}
	if probeBody == nil {
		t.Fatalf("expected probe request to be captured")
	}
	if len(probeBody) >= len(body) {
		t.Fatalf("probe body (%d bytes) should be smaller than full body (%d bytes)", len(probeBody), len(body))
	}
}

// TestExecuteOnceWithRetriesExhaustsOnNon429Retryable verifies that a
// retryable non-429 error (e.g. HTTP 503) triggers the probe path, and
// maxRetries bounds the number of full requests.  Probes succeed but full
// requests keep failing until the retry limit is exhausted.
func TestExecuteOnceWithRetriesExhaustsOnNon429Retryable(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	fullReqCount := 0
	probeReqCount := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := payload.MaxTokens == 1 && len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeReqCount++
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fullReqCount++
		header := make(http.Header)
		header.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"error":"unavailable"}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hello"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	_, err = executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if fullReqCount != maxRetries {
		t.Fatalf("expected %d full requests, got %d", maxRetries, fullReqCount)
	}
	if probeReqCount != maxRetries-1 {
		t.Fatalf("expected %d probe requests, got %d", maxRetries-1, probeReqCount)
	}
	if !strings.Contains(err.Error(), "retry limit exhausted") {
		t.Fatalf("expected retry-exhausted error, got: %v", err)
	}
}

// TestExecuteOnceWithRetriesProbesOnNon429 verifies that a retryable
// non-429 error (e.g. HTTP 503) triggers the probe path.  The first full
// request gets 503, the probe succeeds (possibly after some retries), and
// the second full request succeeds.
func TestExecuteOnceWithRetriesProbesOnNon429(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	fullReqCount := 0
	probeReqCount := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		_ = req.Body.Close()
		var payload struct {
			Messages  []Message `json:"messages"`
			MaxTokens int       `json:"max_tokens"`
		}
		_ = json.Unmarshal(body, &payload)
		isProbe := payload.MaxTokens == 1 && len(payload.Messages) == 1 && payload.Messages[0].Content == "."
		if isProbe {
			probeReqCount++
			if probeReqCount < 2 {
				header := make(http.Header)
				header.Set("Retry-After", "0")
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Header:     header,
					Body:       io.NopCloser(strings.NewReader(`{"error":"probe unavailable"}`)),
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"pong"}}]}`)),
				Request:    req,
			}, nil
		}
		fullReqCount++
		if fullReqCount == 1 {
			header := make(http.Header)
			header.Set("Retry-After", "0")
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(`{"error":"unavailable"}`)),
				Request:    req,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"success"}}]}`)),
			Request:    req,
		}, nil
	})

	body, err := encodePayload(map[string]any{
		"model":    "test-model",
		"messages": []Message{{Role: "user", Content: "hello"}},
	}, false)
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	got, err := executeOnceWithRetries(context.Background(), model, body, llmAttemptMeta{})
	if err != nil {
		t.Fatalf("executeOnceWithRetries error: %v", err)
	}
	if got != "success" {
		t.Fatalf("unexpected result: %q", got)
	}
	if fullReqCount != 2 {
		t.Fatalf("expected 2 full requests, got %d", fullReqCount)
	}
	if probeReqCount != 2 {
		t.Fatalf("expected 2 probe requests, got %d", probeReqCount)
	}
}

// TestProbeUntilReadyTimesOut verifies that probeUntilReady returns a timeout
// error when the server never returns 200.
func TestProbeUntilReadyTimesOut(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	defer func() { http.DefaultClient.Transport = originalTransport }()

	// Override probe timeout to a short duration for testing.
	originalProbeTimeout := probeTimeoutOverride
	probeTimeoutOverride = 50 * time.Millisecond
	defer func() { probeTimeoutOverride = originalProbeTimeout }()

	attempts := 0
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"rate limited"}`)),
			Request:    req,
		}, nil
	})

	model := ResolvedModel{Alias: "primary", BaseURL: "http://example.com", Endpoint: "/chat/completions", APIKey: "test", Model: "test-model"}
	err := probeUntilReady(context.Background(), model)
	if err == nil {
		t.Fatal("expected timeout error from probeUntilReady")
	}
	if !strings.Contains(err.Error(), "probe timed out") && !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected timeout-related error, got: %v", err)
	}
	if attempts < 1 {
		t.Fatalf("expected at least 1 probe attempt, got %d", attempts)
	}
}
