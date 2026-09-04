package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
