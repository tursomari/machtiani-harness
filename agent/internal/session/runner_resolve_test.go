package session

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestPrepareRunBootstrapResolvesShortSessionID(t *testing.T) {
	setupResolveSessionTest(t)
	sessionID := "DM1-KYF1E4CZE7XRSTU123456789ABCD"
	writeResolveSessionConversation(t, sessionID)

	bootstrap, result, ok := prepareRunBootstrap(context.Background(), Options{
		Config: Config{SessionID: "dm1-kyf1e-4cze7x"},
	}, io.Discard)
	if !ok {
		t.Fatalf("prepareRunBootstrap failed: %+v", result)
	}
	if bootstrap.sessionID != sessionID {
		t.Fatalf("bootstrap sessionID = %q, want %q", bootstrap.sessionID, sessionID)
	}
	if bootstrap.opts.Config.SessionID != sessionID {
		t.Fatalf("bootstrap config SessionID = %q, want %q", bootstrap.opts.Config.SessionID, sessionID)
	}
	if !bootstrap.resumeMode {
		t.Fatal("bootstrap resumeMode = false, want true")
	}
}

func TestPrepareRunBootstrapUnknownShortSessionFailsClosed(t *testing.T) {
	setupResolveSessionTest(t)
	var diag bytes.Buffer

	bootstrap, result, ok := prepareRunBootstrap(context.Background(), Options{
		Config: Config{SessionID: "dm1-unknown"},
		Goal:   "Resume prompt",
	}, &diag)
	if ok || bootstrap != nil {
		t.Fatalf("prepareRunBootstrap = %#v, ok %t; want failure", bootstrap, ok)
	}
	if result.ExitCode != 1 || result.Err == nil {
		t.Fatalf("result = %+v, want exit code 1 with error", result)
	}
	if !strings.Contains(diag.String(), "unknown session") {
		t.Fatalf("diagnostics = %q, want unknown session error", diag.String())
	}
}

func TestPrepareRunBootstrapGeneratesNewIDWithoutResolver(t *testing.T) {
	setupResolveSessionTest(t)

	bootstrap, result, ok := prepareRunBootstrap(context.Background(), Options{
		Goal: "Start a new session",
	}, io.Discard)
	if !ok {
		t.Fatalf("prepareRunBootstrap failed: %+v", result)
	}
	if bootstrap.sessionID == "" {
		t.Fatal("bootstrap generated an empty session ID")
	}
	if bootstrap.resumeMode {
		t.Fatal("bootstrap resumeMode = true for a new session")
	}
}
