package llm

import (
	"context"
	"encoding/json"
	"errors"
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
printf '%s\n' '{"v":1,"id":"generation","event":{"type":"usage","inputTokens":7,"outputTokens":2,"totalTokens":9,"cacheReadTokens":3}}'
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
	if !observed.UsageAvailable || observed.PromptTokens != 7 || observed.CompletionTokens != 2 || observed.CachedTokens != 3 {
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

func TestModelHostTransportReturnsStructuredFailure(t *testing.T) {
	fixture := writeModelHostFixture(t, `
read request
printf '%s\n' '{"v":1,"id":"generation","error":{"code":"AUTH_EXPIRED","message":"Sign in again."}}'
`)
	_, err := chatModelHost(context.Background(), ResolvedModel{
		Transport: "model-host", Profile: "/private/profile.json", Command: fixture, Model: "fixture",
	}, nil, []Message{{Role: "user", Content: "hello"}}, false, nil)
	var hostErr *ModelHostCallError
	if err == nil || !strings.Contains(err.Error(), "Sign in again") || !errors.As(err, &hostErr) || hostErr.Code != "AUTH_EXPIRED" {
		t.Fatalf("error=%#v", err)
	}
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
