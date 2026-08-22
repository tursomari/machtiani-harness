package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/shellaction"
)

func TestRunAttachTailsRunningSessionWithoutDuplicates(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-tail-loop"
	conv := conversation.New(sessionID, "Tail the live session")
	addAttachTailTurn(conv, 1)
	path := writeAttachTailConversation(t, conv)

	var active atomic.Bool
	active.Store(true)
	writerDone := make(chan error, 1)
	go func() {
		time.Sleep(15 * time.Millisecond)
		conv.AddMessage("assistant", "Live question 2", map[string]any{
			"type":     "work_request",
			"turn":     2,
			"decision": "Live decision 2",
		})
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}

		time.Sleep(15 * time.Millisecond)
		conv.AddMessage("assistant", "Live answer 2", map[string]any{"type": "work_result", "turn": 2})
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}

		time.Sleep(15 * time.Millisecond)
		addAttachTailTurn(conv, 3)
		if err := publishAttachTailConversation(path, conv); err != nil {
			writerDone <- err
			return
		}
		time.Sleep(15 * time.Millisecond)
		active.Store(false)
		writerDone <- nil
	}()

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile: os.ReadFile,
		probe: func(string) (bool, error) {
			return active.Load(), nil
		},
		pollInterval: 5 * time.Millisecond,
		quietGrace:   20 * time.Millisecond,
	})
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("runAttachWithDependencies() exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	output := stdout.String()
	for _, want := range []string{
		"Tail the live session",
		"Live question 1",
		"Live answer 1",
		"Live question 2",
		"Live answer 2",
		"Live question 3",
		"Live answer 3",
	} {
		if got := strings.Count(output, want); got != 1 {
			t.Errorf("stdout count for %q = %d, want 1:\n%s", want, got, output)
		}
	}
	for turn := 1; turn <= 3; turn++ {
		heading := fmt.Sprintf("──── TURN %d ────", turn)
		if got := strings.Count(output, heading); got != 1 {
			t.Errorf("stdout count for %q = %d, want 1:\n%s", heading, got, output)
		}
	}
}

func TestRunAttachReportsMidLoopFailures(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-tail-errors"
	initial := conversation.New(sessionID, "Tail errors are reported")
	addAttachTailTurn(initial, 1)
	initialData, err := initial.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	unknown := conversation.New(sessionID, "Tail errors are reported")
	addAttachTailTurn(unknown, 1)
	unknown.AddMessage("assistant", "Cannot render this", map[string]any{"type": "unknown_tail_type"})
	unknownData, err := unknown.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		nextData   []byte
		readErr    error
		wantStderr string
	}{
		{name: "read", readErr: errors.New("tail read failed"), wantStderr: "tail read failed"},
		{name: "unmarshal", nextData: []byte("{invalid json"), wantStderr: "unmarshal conversation"},
		{name: "render", nextData: unknownData, wantStderr: `unhandled message type "unknown_tail_type"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reads := 0
			readFile := func(string) ([]byte, error) {
				reads++
				if reads == 1 {
					return initialData, nil
				}
				return tt.nextData, tt.readErr
			}

			var stdout, stderr strings.Builder
			code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
				readFile:     readFile,
				probe:        func(string) (bool, error) { return true, nil },
				pollInterval: time.Millisecond,
				quietGrace:   5 * time.Millisecond,
			})
			if code != 1 {
				t.Fatalf("runAttachWithDependencies() exit = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Fatalf("stderr = %q, want error containing %q", stderr.String(), tt.wantStderr)
			}
			if got := strings.Count(stdout.String(), "Tail errors are reported"); got != 1 {
				t.Fatalf("initial snapshot count = %d, want 1: %q", got, stdout.String())
			}
		})
	}
}

func TestRunAttachRendersPersistedShellActionsByDefault(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-actions-finished"
	conv := conversation.New(sessionID, "Replay persisted actions")
	addAttachTailTurn(conv, 1)
	writeAttachTailConversation(t, conv)
	writeAttachActions(t, sessionID, 1,
		shellaction.Record{Version: 1, SessionID: sessionID, Turn: 1, Sequence: 2, Description: "Run tests", Command: "go test ./..."},
		shellaction.Record{Version: 1, SessionID: sessionID, Turn: 1, Sequence: 1, Description: "Inspect status", Command: "git status"},
	)

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		readActions:  os.ReadFile,
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{"── SHELL STEPS ──", "$ git status", "$ go test ./..."} {
		if got := strings.Count(output, want); got != 1 {
			t.Errorf("stdout count for %q = %d, want 1:\n%s", want, got, output)
		}
	}
	if strings.Index(output, "$ git status") >= strings.Index(output, "$ go test ./...") {
		t.Fatalf("actions not rendered in sequence order:\n%s", output)
	}
}

func TestRunAttachNoShellStepsDoesNotReadJournal(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-actions-hidden"
	conv := conversation.New(sessionID, "Hide persisted actions")
	addAttachTailTurn(conv, 1)
	writeAttachTailConversation(t, conv)
	legacy, err := conversation.RenderReplay(conv)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile: os.ReadFile,
		readActions: func(string) ([]byte, error) {
			t.Fatal("actions journal read with --no-shell-steps")
			return nil, nil
		},
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: time.Millisecond,
		quietGrace:   3 * time.Millisecond,
		noShellSteps: true,
	})
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSuffix(stdout.String(), "\n"); got != legacy {
		t.Fatalf("suppressed output changed legacy replay:\nlegacy: %q\ngot:    %q", legacy, got)
	}
}

func TestRunAttachTailsLateShellActionAndExtendsQuietGrace(t *testing.T) {
	setupSessionArchiveCommandTest(t)
	sessionID := "agent-attach-actions-live"
	conv := conversation.New(sessionID, "Tail late actions")
	addAttachTailTurn(conv, 1)
	writeAttachTailConversation(t, conv)

	writerDone := make(chan error, 1)
	go func() {
		time.Sleep(15 * time.Millisecond)
		writerDone <- appendAttachAction(sessionID, 1, shellaction.Record{
			Version: 1, SessionID: sessionID, Turn: 1, Sequence: 1,
			Description: "Late announced action", Command: "printf late",
		})
	}()

	started := time.Now()
	var stdout, stderr strings.Builder
	code := runAttachWithDependencies(sessionID, &stdout, &stderr, attachDependencies{
		readFile:     os.ReadFile,
		readActions:  os.ReadFile,
		probe:        func(string) (bool, error) { return false, nil },
		pollInterval: 2 * time.Millisecond,
		quietGrace:   30 * time.Millisecond,
	})
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("runAttachWithDependencies() = %d, stderr=%q", code, stderr.String())
	}
	if got := strings.Count(stdout.String(), "$ printf late"); got != 1 {
		t.Fatalf("late action count = %d, want 1:\n%s", got, stdout.String())
	}
	if elapsed := time.Since(started); elapsed < 40*time.Millisecond {
		t.Fatalf("attach exited after %s; late action did not extend quiet grace", elapsed)
	}
}

func addAttachTailTurn(conv *conversation.Conversation, turn int) {
	conv.AddMessage("assistant", fmt.Sprintf("Live question %d", turn), map[string]any{
		"type":     "work_request",
		"turn":     turn,
		"decision": fmt.Sprintf("Live decision %d", turn),
	})
	conv.AddMessage("assistant", fmt.Sprintf("Live answer %d", turn), map[string]any{
		"type": "work_result",
		"turn": turn,
	})
}

func writeAttachTailConversation(t *testing.T, conv *conversation.Conversation) string {
	t.Helper()
	path, err := artifacts.SessionConversationFile(conv.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := publishAttachTailConversation(path, conv); err != nil {
		t.Fatal(err)
	}
	return path
}

func publishAttachTailConversation(path string, conv *conversation.Conversation) error {
	data, err := conv.Marshal()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".conversation-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func writeAttachActions(t *testing.T, sessionID string, turn int, records ...shellaction.Record) string {
	t.Helper()
	path, err := artifacts.ShellAgentTrajectoryPath(sessionID, turn)
	if err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(filepath.Dir(path), "actions.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if err := appendAttachAction(sessionID, turn, record); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func appendAttachAction(sessionID string, turn int, record shellaction.Record) error {
	trajectoryPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, turn)
	if err != nil {
		return err
	}
	path := filepath.Join(filepath.Dir(trajectoryPath), "actions.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := shellaction.EncodeRecord(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.Write(data)
	return err
}
