package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
)

func TestResolveSessionCanonicalizesQueryAndNames(t *testing.T) {
	setupResolveSessionTest(t)
	sessionID := "DM1-KYF1E4CZE7XRSTU123456789ABCD"
	writeResolveSessionConversation(t, sessionID)

	for _, query := range []string{
		"dm1-kyf1e-4cze7x",
		"dm1kyf1e4cze7x",
		"dm1-kyf1e4cze7xrstu123456789abcd",
	} {
		t.Run(query, func(t *testing.T) {
			got, err := ResolveSessionID(query)
			if err != nil {
				t.Fatalf("ResolveSessionID(%q): %v", query, err)
			}
			if got != sessionID {
				t.Fatalf("ResolveSessionID(%q) = %q, want %q", query, got, sessionID)
			}
		})
	}
}

func TestResolveSessionExactMatchWinsOverPrefixAmbiguity(t *testing.T) {
	setupResolveSessionTest(t)
	exactID := "DM1-KYF1E4CZE7X"
	writeResolveSessionConversation(t, exactID)
	writeResolveSessionConversation(t, exactID+"EXTRA")

	got, err := ResolveSessionID("dm1-kyf1e-4cze7x")
	if err != nil {
		t.Fatalf("ResolveSessionID: %v", err)
	}
	if got != exactID {
		t.Fatalf("ResolveSessionID = %q, want exact match %q", got, exactID)
	}
}

func TestResolveSessionUnknown(t *testing.T) {
	setupResolveSessionTest(t)
	query := "dm1-zzzzz-zzzzzz"

	got, err := ResolveSessionID(query)
	if got != "" {
		t.Fatalf("ResolveSessionID returned %q, want empty result", got)
	}
	if err == nil || !strings.Contains(err.Error(), "unknown session") || !strings.Contains(err.Error(), query) {
		t.Fatalf("ResolveSessionID error = %v, want unknown error containing %q", err, query)
	}
}

func TestResolveSessionAmbiguousListsCandidates(t *testing.T) {
	setupResolveSessionTest(t)
	firstID := "ABC-DEF-111"
	secondID := "ABC-DEF-112"
	writeResolveSessionConversation(t, secondID)
	writeResolveSessionConversation(t, firstID)

	_, err := ResolveSessionID("abc-def-11")
	if err == nil {
		t.Fatal("ResolveSessionID returned nil error, want ambiguity")
	}
	wantCandidates := firstID + ", " + secondID
	if !strings.Contains(err.Error(), wantCandidates) {
		t.Fatalf("ResolveSessionID error = %q, want sorted candidates %q", err, wantCandidates)
	}
}

func TestResolveSessionAmbiguousNeverPicksSilently(t *testing.T) {
	setupResolveSessionTest(t)
	writeResolveSessionConversation(t, "ABC-DEF-111")
	writeResolveSessionConversation(t, "ABC-DEF-112")

	got, err := ResolveSessionID("abc-def-11")
	if got != "" {
		t.Fatalf("ResolveSessionID returned ambiguous winner %q", got)
	}
	if err == nil {
		t.Fatal("ResolveSessionID returned nil error for ambiguous query")
	}
}

func TestResolveSessionTreatsIDsAsOpaqueText(t *testing.T) {
	setupResolveSessionTest(t)
	firstAgentID := "agent-20260821-1842"
	secondAgentID := "agent-20300101-9999"
	uuidID := "3f6ae792-6a62-4aee-8673-4cc631624ecc"
	for _, sessionID := range []string{firstAgentID, secondAgentID, uuidID} {
		writeResolveSessionConversation(t, sessionID)
	}

	got, err := ResolveSessionID("agent-2026")
	if err != nil || got != firstAgentID {
		t.Fatalf("ResolveSessionID(agent-2026) = %q, %v; want %q, nil", got, err, firstAgentID)
	}

	got, err = ResolveSessionID("agent-20")
	if got != "" || err == nil {
		t.Fatalf("ResolveSessionID(agent-20) = %q, %v; want empty result and ambiguity", got, err)
	}
	if !strings.Contains(err.Error(), firstAgentID) || !strings.Contains(err.Error(), secondAgentID) {
		t.Fatalf("ResolveSessionID(agent-20) error = %q, want both agent IDs", err)
	}

	got, err = ResolveSessionID("3f6ae792")
	if err != nil || got != uuidID {
		t.Fatalf("ResolveSessionID(3f6ae792) = %q, %v; want %q, nil", got, err, uuidID)
	}
}

func TestResolveSessionRejectsEmptyQuery(t *testing.T) {
	setupResolveSessionTest(t)
	for _, query := range []string{"", " \t\n "} {
		got, err := ResolveSessionID(query)
		if got != "" {
			t.Fatalf("ResolveSessionID(%q) returned %q, want empty result", query, got)
		}
		if err == nil || err.Error() != "session id required" {
			t.Fatalf("ResolveSessionID(%q) error = %v, want session id required", query, err)
		}
	}
}

func TestResolveSessionMissingRootIsUnknown(t *testing.T) {
	setupResolveSessionTest(t)
	query := "dm1-missing"

	got, err := ResolveSessionID(query)
	if got != "" {
		t.Fatalf("ResolveSessionID returned %q, want empty result", got)
	}
	if err == nil || err.Error() != `unknown session: "dm1-missing"` {
		t.Fatalf("ResolveSessionID error = %v, want unknown-session error", err)
	}
}

func setupResolveSessionTest(t *testing.T) {
	t.Helper()
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("MACHTIANI_SESSION_ID", "")
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
}

func writeResolveSessionConversation(t *testing.T, sessionID string) string {
	t.Helper()
	conv := conversation.New(sessionID, "Resolver fixture")
	conv.Goal = "Resolver fixture"
	conv.Status = "completed"
	data, err := conv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
