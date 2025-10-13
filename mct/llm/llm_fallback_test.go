package llm

import (
	"context"
	"io"
	"net/http"
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
