package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

func TestArchiveSessionByID(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	sessionID := "agent-archive-by-id"
	convPath := writeArchiveTestConversation(t, sessionID, "Archive this session")
	sessionDir := filepath.Dir(convPath)

	if err := ArchiveSession(sessionID); err != nil {
		t.Fatalf("ArchiveSession() error = %v", err)
	}

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("ListSessions() returned archived session: %#v", sessions)
	}

	data, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read archived conversation: %v", err)
	}
	archived, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal archived conversation: %v", err)
	}
	if !archived.Archived {
		t.Fatal("conversation Archived = false, want true")
	}
	if info, err := os.Stat(sessionDir); err != nil || !info.IsDir() {
		t.Fatalf("session directory was moved or deleted: info=%v err=%v", info, err)
	}

	if err := ArchiveSession(sessionID); err != nil {
		t.Fatalf("ArchiveSession() on already archived session error = %v", err)
	}
	if err := ArchiveSession("agent-does-not-exist"); err == nil {
		t.Fatal("ArchiveSession() unknown ID error = nil, want error")
	}
}

func TestUnarchiveSessionByID(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	sessionID := "agent-unarchive-by-id"
	convPath := writeArchiveTestConversation(t, sessionID, "Restore this session")
	if err := ArchiveSession(sessionID); err != nil {
		t.Fatalf("ArchiveSession() error = %v", err)
	}

	if err := UnarchiveSession(sessionID); err != nil {
		t.Fatalf("UnarchiveSession() error = %v", err)
	}

	data, err := os.ReadFile(convPath)
	if err != nil {
		t.Fatalf("read unarchived conversation: %v", err)
	}
	unarchived, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal unarchived conversation: %v", err)
	}
	if unarchived.Archived {
		t.Fatal("conversation Archived = true, want false")
	}

	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].SessionID != sessionID {
		t.Fatalf("ListSessions() = %#v, want restored session %q", sessions, sessionID)
	}

	if err := UnarchiveSession(sessionID); err != nil {
		t.Fatalf("UnarchiveSession() on active session error = %v", err)
	}
	if err := UnarchiveSession("agent-does-not-exist"); err == nil {
		t.Fatal("UnarchiveSession() unknown ID error = nil, want error")
	}
}

func writeArchiveTestConversation(t *testing.T, sessionID, goal string) string {
	t.Helper()
	conv := conversation.New(sessionID, goal)
	conv.Goal = goal
	conv.Status = "completed"
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir session directory: %v", err)
	}
	if err := os.WriteFile(convPath, data, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}
	return convPath
}
