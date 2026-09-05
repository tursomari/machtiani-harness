package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func captureModelHostRequest(t *testing.T) (string, string) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "request.json")
	t.Setenv("MACHTIANI_MODEL_HOST_CAPTURE", capture)
	fixture := writeModelHostFixture(t, `
read request
printf '%s' "$request" > "$MACHTIANI_MODEL_HOST_CAPTURE"
printf '%s\n' '{"v":1,"id":"generation","result":{"completed":true}}'
`)
	return fixture, capture
}

func writeModelHostFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "model-host")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestModelHostTransportStreamsAndReportsUsage(t *testing.T) {
	fixture := writeModelHostFixture(t, `
read request
case "$request" in
  *SUPER_SECRET*) exit 91 ;;
  *\"caller\":\"machtiani\"*) ;;
  *) exit 92 ;;
esac
printf '%s\n' '{"v":1,"id":"generation","event":{"type":"text-delta","index":0,"text":"hello "}}'
printf '%s\n' '{"v":1,"id":"generation","event":{"type":"text-delta","index":0,"text":"world"}}'
printf '%s\n' '{"v":1,"id":"generation","event":{"type":"usage","inputTokens":7,"outputTokens":2,"totalTokens":9,"cacheReadTokens":3,"reasoningTokens":1}}'
printf '%s\n' '{"v":1,"id":"generation","result":{"completed":true}}'
`)
	model := ResolvedModel{Transport: "model-host", Profile: "/private/profile.json", Command: fixture, Model: "fixture"}
	var tokens []string
	var observed UsageInfo
	ctx := WithUsageObserver(context.Background(), func(_ ResolvedModel, usage UsageInfo) { observed = usage })
	answer, err := chatModelHost(ctx, model, nil, []Message{{Role: "user", Content: "not a secret"}}, true, func(token string) {
		tokens = append(tokens, token)
	})
	if err != nil {
		t.Fatal(err)
	}
	if answer != "hello world" || strings.Join(tokens, "") != answer {
		t.Fatalf("answer=%q tokens=%q", answer, tokens)
	}
	if !observed.UsageAvailable || observed.PromptTokens != 7 || observed.CompletionTokens != 2 || observed.CachedTokens != 3 || observed.ReasoningTokens != 1 {
		t.Fatalf("usage=%+v", observed)
	}
}

func TestModelHostTransportPreservesRoleAndCacheBoundary(t *testing.T) {
	fixture, capture := captureModelHostRequest(t)
	model := ResolvedModel{
		Transport: "model-host", Profile: "/private/profile.json", Command: fixture, Model: "fixture",
		CacheKeyName: "cache_control", CacheControl: map[string]any{"type": "ephemeral"},
		CacheTriggerThreshold: 1, CacheLookbackOffset: 2,
	}
	messages := []Message{
		{Role: "system", Content: "stable instructions", Metadata: map[string]any{"estimated_tokens": 20}},
		{Role: "user", Content: "variable suffix", Metadata: map[string]any{"estimated_tokens": 20}},
	}
	ctx := WithStage(context.Background(), "planner")
	if _, err := chatModelHost(ctx, model, nil, messages, false, nil); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Params struct {
			Caller   string `json:"caller"`
			Role     string `json:"role"`
			Messages []struct {
				Role         string         `json:"role"`
				Content      string         `json:"content"`
				CacheControl map[string]any `json:"cacheControl"`
			} `json:"messages"`
		} `json:"params"`
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		t.Fatal(err)
	}
	if request.Params.Caller != "machtiani" || request.Params.Role != "planner" {
		t.Fatalf("identity=%s role=%s", request.Params.Caller, request.Params.Role)
	}
	if len(request.Params.Messages) != 2 || request.Params.Messages[0].Content != "stable instructions" || request.Params.Messages[0].CacheControl["type"] != "ephemeral" {
		t.Fatalf("messages=%#v", request.Params.Messages)
	}
	if request.Params.Messages[1].CacheControl != nil {
		t.Fatalf("variable suffix unexpectedly cacheable: %#v", request.Params.Messages[1])
	}
}

func TestModelHostTransportReturnsStructuredFailures(t *testing.T) {
	tests := []struct {
		code         string
		retryAfterMS int
	}{
		{code: "AUTH_REQUIRED"},
		{code: "AUTH_EXPIRED"},
		{code: "RATE_LIMITED", retryAfterMS: 2750},
		{code: "QUOTA_EXHAUSTED"},
		{code: "MODEL_UNAVAILABLE"},
		{code: "UPSTREAM_CHANGED"},
		{code: "CANCELLED"},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			fixture := writeModelHostFixture(t, fmt.Sprintf(`
read request
			printf '%%s\n' '{"v":1,"id":"generation","error":{"code":"%s","message":"provider detail","retryAfterMs":%d}}'
			`, test.code, test.retryAfterMS))
			_, err := chatModelHost(context.Background(), ResolvedModel{
				Transport: "model-host", Profile: "/private/profile.json", Command: fixture, Model: "fixture",
			}, nil, []Message{{Role: "user", Content: "hello"}}, false, nil)
			var hostErr *ModelHostCallError
			if err == nil || !strings.Contains(err.Error(), "provider detail") || !errors.As(err, &hostErr) || hostErr.Code != test.code || hostErr.RetryAfterMS != test.retryAfterMS {
				t.Fatalf("error=%#v structured=%#v", err, hostErr)
			}
		})
	}
}

func TestModelHostFailureIsActionableOutsideVerify(t *testing.T) {
	tests := []struct {
		fault ModelHostCallError
		want  []string
	}{
		{fault: ModelHostCallError{Code: "AUTH_EXPIRED", Message: "Sign in expired.", ModelAlias: "dearmachine"}, want: []string{"Sign in expired.", "machtiani auth login --model dearmachine"}},
		{fault: ModelHostCallError{Code: "RATE_LIMITED", Message: "Slow down.", RetryAfterMS: 2500}, want: []string{"Slow down.", "retry after 3s"}},
		{fault: ModelHostCallError{Code: "QUOTA_EXHAUSTED", Message: "No usage."}, want: []string{"No usage.", "subscription account's usage limits"}},
		{fault: ModelHostCallError{Code: "MODEL_UNAVAILABLE", Message: "Gone."}, want: []string{"Gone.", "configured model"}},
		{fault: ModelHostCallError{Code: "UPSTREAM_CHANGED", Message: "Protocol changed."}, want: []string{"Protocol changed.", "pinned model-host runtime"}},
	}
	for _, test := range tests {
		t.Run(test.fault.Code, func(t *testing.T) {
			if got := test.fault.Error(); !containsAllStrings(got, test.want...) {
				t.Fatalf("error=%q want=%v", got, test.want)
			}
		})
	}
}

func containsAllStrings(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}

func TestModelHostTransportHonorsCancellation(t *testing.T) {
	fixture := writeModelHostFixture(t, "read request\nexec sleep 30\n")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := chatModelHost(ctx, ResolvedModel{
		Transport: "model-host", Profile: "/private/profile.json", Command: fixture, Model: "fixture",
	}, nil, []Message{{Role: "user", Content: "hello"}}, false, nil)
	if err == nil || time.Since(started) > 3*time.Second {
		t.Fatalf("error=%v elapsed=%s", err, time.Since(started))
	}
}

func TestModelHostPrimaryUsesTransportWithSelfFallbacks(t *testing.T) {
	fixture := writeModelHostFixture(t, `
read request
printf '%s\n' '{"v":1,"id":"generation","event":{"type":"text-delta","index":0,"text":"host response"}}'
printf '%s\n' '{"v":1,"id":"generation","result":{"completed":true}}'
`)
	primary := ResolvedModel{
		Alias: "dearmachine", Transport: "model-host", Profile: "/private/profile.json", Command: fixture, Model: "fixture",
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	answer, err := ChatWithResolvedFallback(ctx, primary, []string{"dearmachine"}, []ResolvedModel{primary}, nil, []Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("ChatWithResolvedFallback error: %v", err)
	}
	if answer != "host response" {
		t.Fatalf("answer = %q, want host response", answer)
	}
}

func TestModelHostPrimaryFailureUsesHTTPFallback(t *testing.T) {
	called := filepath.Join(t.TempDir(), "model-host-called")
	t.Setenv("MACHTIANI_MODEL_HOST_CALLED", called)
	fixture := writeModelHostFixture(t, `
read request
: > "$MACHTIANI_MODEL_HOST_CALLED"
printf '%s\n' '{"v":1,"id":"generation","error":{"code":"MODEL_UNAVAILABLE","message":"primary unavailable"}}'
`)
	primary := ResolvedModel{
		Alias: "dearmachine", Transport: "model-host", Profile: "/private/profile.json", Command: fixture, Model: "host-model",
	}
	fallback := ResolvedModel{
		Alias: "fallback", BaseURL: "http://fallback.example", Endpoint: "/chat/completions", Model: "fallback-model",
	}
	originalTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"fallback response"}}]}`)),
			Request:    req,
		}, nil
	})
	defer func() { http.DefaultClient.Transport = originalTransport }()

	answer, err := ChatWithResolvedFallback(context.Background(), primary, nil, []ResolvedModel{fallback}, nil, []Message{{Role: "user", Content: "hello"}})
	if err != nil {
		t.Fatalf("ChatWithResolvedFallback error: %v", err)
	}
	if answer != "fallback response" {
		t.Fatalf("answer = %q, want fallback response", answer)
	}
	if _, err := os.Stat(called); err != nil {
		t.Fatalf("model-host primary was not called: %v", err)
	}
}
