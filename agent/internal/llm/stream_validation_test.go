package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPerformStreamRejectsInvalidOrEmptyResponses(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, wantError, wantPartial string
	}{
		{"ordinary JSON", "application/json", `{"choices":[{"message":{"content":"answer"}}]}`, "expected Content-Type", ""},
		{"JSON without header", "", `{"choices":[{"message":{"content":"answer"}}]}`, ErrEmptyResponse.Error(), ""},
		{"empty body", "text/event-stream", "", ErrEmptyResponse.Error(), ""},
		{"done only", "text/event-stream", "data: [DONE]\n\n", ErrEmptyResponse.Error(), ""},
		{"role and usage only", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"completion_tokens\":1}}\n\ndata: [DONE]\n\n", ErrEmptyResponse.Error(), ""},
		{"whitespace answer", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\" \\n\"}}]}\n\ndata: [DONE]\n\n", ErrEmptyResponse.Error(), " \n"},
		{"malformed event", "text/event-stream", "data: {broken}\n\n", "invalid streaming response event", ""},
		{"malformed after content", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {broken}\n\n", "invalid streaming response event", "hello"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := streamingHTTPClient
			t.Cleanup(func() { streamingHTTPClient = original })
			streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {tc.contentType}},
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Request:    req,
				}, nil
			})}
			req, err := http.NewRequest(http.MethodPost, "http://example.invalid/chat", nil)
			if err != nil {
				t.Fatal(err)
			}
			var emitted strings.Builder
			result, _, err := performStream(req, func(token string) { emitted.WriteString(token) })
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if result != tc.wantPartial || emitted.String() != tc.wantPartial {
				t.Fatalf("result/emitted = %q/%q, want %q", result, emitted.String(), tc.wantPartial)
			}
		})
	}
}

func TestStreamValidationAndFallback(t *testing.T) {
	const answer = "hello world"
	for _, tc := range []struct {
		name, contentType, body string
		fallback, emptyFallback bool
	}{
		{"JSON response", "application/json", `{"choices":[{"message":{"content":"ignored JSON"}}]}`, true, false},
		{"empty stream", "text/event-stream", "data: [DONE]\n\n", true, false},
		{"malformed event", "text/event-stream", "data: {broken}\n\n", true, false},
		{"partial then malformed", "text/event-stream", "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {broken}\n\n", true, false},
		{"empty fallback", "text/event-stream", "data: [DONE]\n\n", true, true},
		{"valid stream", "text/event-stream; charset=utf-8", ": heartbeat\n\ndata: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata:{\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"completion_tokens\":2}}\n\ndata: [DONE]\n\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var streamCalls, fallbackCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Stream bool `json:"stream"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if payload.Stream {
					streamCalls.Add(1)
					w.Header().Set("Content-Type", tc.contentType)
					io.WriteString(w, tc.body)
					return
				}
				fallbackCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if tc.emptyFallback {
					io.WriteString(w, `{"choices":[{"message":{"content":" \n"}}]}`)
				} else {
					io.WriteString(w, `{"choices":[{"message":{"content":"hello world"}}]}`)
				}
			}))
			defer server.Close()
			model := ResolvedModel{Alias: "primary", BaseURL: server.URL, Endpoint: "/chat/completions", Model: "test-model"}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var emitted strings.Builder
			result, err := tryStreamThenFallback(ctx, model, []byte(`{"stream":true}`), []byte(`{"stream":false}`), func(token string) { emitted.WriteString(token) })
			if tc.emptyFallback {
				if !errors.Is(err, ErrEmptyResponse) || result != "" {
					t.Fatalf("result/error = %q/%v, want empty-response failure", result, err)
				}
			} else if err != nil || result != answer || emitted.String() != answer {
				t.Fatalf("result/emitted/error = %q/%q/%v, want %q", result, emitted.String(), err, answer)
			}
			wantFallback := int32(0)
			if tc.fallback {
				wantFallback = 1
			}
			if streamCalls.Load() != 1 || fallbackCalls.Load() != wantFallback {
				t.Fatalf("stream/fallback calls = %d/%d, want 1/%d", streamCalls.Load(), fallbackCalls.Load(), wantFallback)
			}
		})
	}
}
