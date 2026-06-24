package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// TestHasNewInputPropagatesToTurnContext is a baseline check that the
// existing plumbing works in isolation. It creates a session state with
// ShellAgentResumable: true, then calls prepareRunBootstrap with
// HasNewInput: true and asserts that bootstrap.hasNewInput is true.
func TestHasNewInputPropagatesToTurnContext(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-has-new-input-%d", time.Now().UnixNano())
	conv := conversation.New(sessionID, "test goal")
	conv.Goal = "test goal"
	conv.ShellAgentResumable = true
	conv.ShellAgentTrajectoryPath = "/tmp/dummy"

	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("Marshal conversation: %v", err)
	}
	if err := os.WriteFile(convPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile conversation: %v", err)
	}
	t.Cleanup(func() {
		_ = RemoveSessionState(sessionID)
		dir, _ := artifacts.SessionDirectory(sessionID)
		_ = os.RemoveAll(dir)
	})

	opts := Options{
		Config: Config{
			SessionID: sessionID,
		},
		Goal:        "test goal",
		HasNewInput: true,
	}

	bootstrap, _, ok := prepareRunBootstrap(context.Background(), opts, io.Discard)
	if !ok {
		t.Fatal("prepareRunBootstrap returned not ok")
	}
	if bootstrap == nil {
		t.Fatal("expected non-nil bootstrap")
	}
	if !bootstrap.hasNewInput {
		t.Errorf("expected bootstrap.hasNewInput to be true, got false")
	}
}

// TestBuildShellAgentRequestResumeAttemptFalseWithNewInput is a baseline
// check that buildShellAgentRequest sets ResumeAttempt to false when
// TurnContext.HasNewInput is true.
func TestBuildShellAgentRequestResumeAttemptFalseWithNewInput(t *testing.T) {
	conv := conversation.New("test-session-build-req-hni", "Test goal")
	recorder := &conversationRecorder{conversation: conv}

	lib := &shellagent.ShellAgentLibrary{
		ExtraInstructions: "extra instructions",
		CommandTag:        "command",
		AnswerTag:         "answer",
		Prompts: &minisweagent.PromptsConfig{
			Planner: &minisweagent.PlannerPromptsConfig{
				SystemTemplate: "Planner system template placeholder",
			},
			ShellAgent: &minisweagent.ShellAgentPromptsConfig{
				SystemTemplate:   "Shell-agent system template placeholder",
				InstanceTemplate: "Instance template for task: {{.Task}}",
			},
		},
		Config: &llm.ShellAgentConfig{StepLimit: 10},
	}

	tc := &TurnContext{
		HasNewInput:  true,
		TurnIndex:    1,
		Conversation: recorder,
		ShellAgentLib: lib,
	}

	req, err := tc.buildShellAgentRequest("dummy task", "test-session-build-req-hni", false, 4000)
	if err != nil {
		t.Fatalf("buildShellAgentRequest: %v", err)
	}
	if req.ResumeAttempt {
		t.Errorf("expected ResumeAttempt to be false when HasNewInput is true, got true")
	}
}

// TestResumableShellAgentFalseWhenHasNewInput verifies that when
// Options.HasNewInput is true, prepareRunBootstrap sets
// bootstrap.resumableShellAgent to false even when the loaded session
// state has ShellAgentResumable: true.
// This test will FAIL initially because runner_state.go does not yet
// gate on HasNewInput.
func TestResumableShellAgentFalseWhenHasNewInput(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-resumable-false-hni-%d", time.Now().UnixNano())
	conv := conversation.New(sessionID, "test goal")
	conv.Goal = "test goal"
	conv.ShellAgentResumable = true
	conv.ShellAgentTrajectoryPath = "/tmp/dummy"

	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("Marshal conversation: %v", err)
	}
	if err := os.WriteFile(convPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile conversation: %v", err)
	}
	t.Cleanup(func() {
		_ = RemoveSessionState(sessionID)
		dir, _ := artifacts.SessionDirectory(sessionID)
		_ = os.RemoveAll(dir)
	})

	opts := Options{
		Config: Config{
			SessionID: sessionID,
		},
		Goal:        "test goal",
		HasNewInput: true,
	}

	bootstrap, _, ok := prepareRunBootstrap(context.Background(), opts, io.Discard)
	if !ok {
		t.Fatal("prepareRunBootstrap returned not ok")
	}
	if bootstrap == nil {
		t.Fatal("expected non-nil bootstrap")
	}
	if bootstrap.resumableShellAgent {
		t.Errorf("expected bootstrap.resumableShellAgent to be false when HasNewInput is true, but got true")
	}
}

// TestResumeWithNewInputSetsHasNewInput is the main RED test. It sets
// up a full session with an interrupted shell-agent work_request,
// then calls prepareRunBootstrap with HasNewInput: true and verifies
// that bootstrap.resumableShellAgent is false.
// This test will FAIL until runner_state.go gates resumableShellAgent
// on HasNewInput.
func TestResumeWithNewInputSetsHasNewInput(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-resume-hni-%d", time.Now().UnixNano())

	// Create a conversation with an interrupted work_request.
	conv := conversation.New(sessionID, "test goal")
	conv.Goal = "test goal"
	conv.ShellAgentResumable = true
	conv.ShellAgentTrajectoryPath = "/tmp/dummy"
	conv.AddMessage("assistant", "Run a background check on the project", map[string]any{
		"type":                        "work_request",
		"turn":                        1,
		"decision":                    "ask_worker",
		"shell_agent_session_id":       sessionID + "/shell-agent/1",
		"shell_agent_trajectory_path": "/tmp/dummy",
		"shell_agent_resumable":       true,
	})

	// Save the conversation to disk.
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("SessionConversationFile: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("mkdir conv dir: %v", err)
	}
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("Marshal conversation: %v", err)
	}
	if err := os.WriteFile(convPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile conversation: %v", err)
	}
	t.Cleanup(func() {
		_ = RemoveSessionState(sessionID)
		dir, _ := artifacts.SessionDirectory(sessionID)
		_ = os.RemoveAll(dir)
	})

	opts := Options{
		Config: Config{
			SessionID: sessionID,
		},
		Goal:            "test goal",
		HasNewInput:     true,
		PlannerOverride: &mockPlanner{},
	}

	bootstrap, _, ok := prepareRunBootstrap(context.Background(), opts, io.Discard)
	if !ok {
		t.Fatal("prepareRunBootstrap returned not ok")
	}
	if bootstrap == nil {
		t.Fatal("expected non-nil bootstrap")
	}
	if bootstrap.resumableShellAgent {
		t.Errorf("expected bootstrap.resumableShellAgent to be false when HasNewInput is true, but got true")
	}
}
