package session

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

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

func TestArchiveSessionsDateRange(t *testing.T) {
	setupArchiveTestWorkingDirectory(t)

	since := time.Date(2026, time.January, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, time.January, 20, 0, 0, 0, 0, time.UTC)
	writeArchiveTestConversationAt(t, "agent-before-range", "Before", since.Add(-time.Nanosecond), false)
	writeArchiveTestConversationAt(t, "agent-range-start", "Start", since, false)
	writeArchiveTestConversationAt(t, "agent-range-end", "End", until, false)
	writeArchiveTestConversationAt(t, "agent-already-archived", "Skipped", since.Add(time.Hour), true)
	writeArchiveTestConversationAt(t, "agent-after-range", "After", until.Add(time.Nanosecond), false)

	report, err := ArchiveSessions(ArchiveOptions{Since: &since, Until: &until})
	if err != nil {
		t.Fatalf("ArchiveSessions() error = %v", err)
	}
	if report.Archived != 2 || report.Skipped != 1 {
		t.Fatalf("ArchiveSessions() report = %#v", report)
	}
	slices.Sort(report.IDs)
	if !slices.Equal(report.IDs, []string{"agent-range-end", "agent-range-start"}) {
		t.Fatalf("ArchiveSessions() IDs = %v", report.IDs)
	}
	assertArchiveTestState(t, "agent-before-range", false)
	assertArchiveTestState(t, "agent-range-start", true)
	assertArchiveTestState(t, "agent-range-end", true)
	assertArchiveTestState(t, "agent-already-archived", true)
	assertArchiveTestState(t, "agent-after-range", false)
}

func TestUnarchiveSessionsDateRange(t *testing.T) {
	setupArchiveTestWorkingDirectory(t)

	since := time.Date(2026, time.February, 10, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, time.February, 20, 0, 0, 0, 0, time.UTC)
	writeArchiveTestConversationAt(t, "agent-before-range", "Before", since.Add(-time.Nanosecond), true)
	writeArchiveTestConversationAt(t, "agent-range-start", "Start", since, true)
	writeArchiveTestConversationAt(t, "agent-range-end", "End", until, true)
	writeArchiveTestConversationAt(t, "agent-already-active", "Skipped", since.Add(time.Hour), false)
	writeArchiveTestConversationAt(t, "agent-after-range", "After", until.Add(time.Nanosecond), true)

	report, err := UnarchiveSessions(ArchiveOptions{Since: &since, Until: &until})
	if err != nil {
		t.Fatalf("UnarchiveSessions() error = %v", err)
	}
	if report.Unarchived != 2 || report.Skipped != 1 {
		t.Fatalf("UnarchiveSessions() report = %#v", report)
	}
	slices.Sort(report.IDs)
	if !slices.Equal(report.IDs, []string{"agent-range-end", "agent-range-start"}) {
		t.Fatalf("UnarchiveSessions() IDs = %v", report.IDs)
	}
	assertArchiveTestState(t, "agent-before-range", true)
	assertArchiveTestState(t, "agent-range-start", false)
	assertArchiveTestState(t, "agent-range-end", false)
	assertArchiveTestState(t, "agent-already-active", false)
	assertArchiveTestState(t, "agent-after-range", true)
}

func writeArchiveTestConversation(t *testing.T, sessionID, goal string) string {
	t.Helper()
	return writeArchiveTestConversationAt(t, sessionID, goal, time.Now().UTC(), false)
}

func writeArchiveTestConversationAt(t *testing.T, sessionID, goal string, updatedAt time.Time, archived bool) string {
	t.Helper()
	conv := conversation.New(sessionID, goal)
	conv.Goal = goal
	conv.Status = "completed"
	conv.UpdatedAt = updatedAt
	conv.Archived = archived
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

func setupArchiveTestWorkingDirectory(t *testing.T) {
	t.Helper()
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
}

func assertArchiveTestState(t *testing.T, sessionID string, wantArchived bool) {
	t.Helper()
	conv, _, err := loadSessionConversation(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if conv.Archived != wantArchived {
		t.Fatalf("session %s Archived = %v, want %v", sessionID, conv.Archived, wantArchived)
	}
}
