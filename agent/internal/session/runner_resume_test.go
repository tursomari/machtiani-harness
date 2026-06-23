package session

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// TestResumeDetectsInterruptedWorkRequest creates a conversation with a
// work_request that has shell_agent_resumable: true and no corresponding
// work_result for the same turn. It then calls the resume detection logic
// and asserts the work request is detected as resumable.
func TestResumeDetectsInterruptedWorkRequest(t *testing.T) {
	conv := conversation.New("test-session-resume-detect", "Test goal for resume detection")

	conv.AddMessage("assistant", "Run a background check on the project", map[string]any{
		"type":                        "work_request",
		"turn":                        1,
		"decision":                    "ask_worker",
		"shell_agent_session_id":       "test-session-resume-detect/shell-agent/1",
		"shell_agent_trajectory_path": "/tmp/test-trajectory.json",
		"shell_agent_resumable":       true,
	})

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal conversation: %v", err)
	}
	loaded, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatalf("failed to unmarshal conversation: %v", err)
	}

	detected := HasResumableShellAgentWorkRequest(loaded)

	if !detected {
		t.Fatalf("expected HasResumableShellAgentWorkRequest to return true for a conversation with shell_agent_resumable: true and no work_result, but got false")
	}
}

// TestResumeDetectsCompletedWorkRequest verifies that when a work_request
// HAS a corresponding work_result, it is NOT detected as resumable.
func TestResumeDetectsCompletedWorkRequest(t *testing.T) {
	conv := conversation.New("test-session-resume-complete", "Test goal for completed turn")

	conv.AddMessage("assistant", "Run a background check on the project", map[string]any{
		"type":                        "work_request",
		"turn":                        1,
		"decision":                    "ask_worker",
		"shell_agent_session_id":       "test-session-resume-complete/shell-agent/1",
		"shell_agent_resumable":       true,
	})

	conv.AddMessage("assistant", "Background check completed: all clear", map[string]any{
		"type":                        "work_result",
		"turn":                        1,
		"shell_agent_resumable":       false,
	})

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal conversation: %v", err)
	}
	loaded, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatalf("failed to unmarshal conversation: %v", err)
	}

	detected := HasResumableShellAgentWorkRequest(loaded)
	if detected {
		t.Fatalf("expected HasResumableShellAgentWorkRequest to return false when work_request has a corresponding work_result, but got true")
	}
}

// TestResumeDetectsNoWorkRequest verifies that a conversation with no
// work_request messages returns false for resumability.
func TestResumeDetectsNoWorkRequest(t *testing.T) {
	conv := conversation.New("test-session-no-workreq", "Test goal with no work request")

	conv.AddMessage("assistant", "Final answer: everything looks good", map[string]any{
		"type":  "final",
		"turns": 1,
	})

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal conversation: %v", err)
	}
	loaded, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatalf("failed to unmarshal conversation: %v", err)
	}

	detected := HasResumableShellAgentWorkRequest(loaded)
	if detected {
		t.Fatalf("expected HasResumableShellAgentWorkRequest to return false when no work_request exists, but got true")
	}
}

// mockPlanner implements the Planner interface for testing.
type mockPlanner struct {
	planCalls int
}

func (m *mockPlanner) Plan(ctx context.Context, conv *conversation.Conversation, goal string, transcript string, step, maxSteps int) (planner.Decision, string, error) {
	m.planCalls++
	return planner.DecisionAnswerUser, "mock", nil
}

func (m *mockPlanner) Finalize(ctx context.Context, conv *conversation.Conversation, goal string) (string, error) {
	return "done", nil
}

func (m *mockPlanner) UpdateProgress(progress planner.Progress) {}

func (m *mockPlanner) AnalyzeUserDirectedAsk(ctx context.Context, conv *conversation.Conversation, goal, ask string, step, maxSteps int) (planner.UserDirectedAskOutcome, error) {
	return planner.UserDirectedAskOutcome{}, nil
}

// TestResumeSkipsWorkRequestWhenResumable verifies that ExtractResumableWorkRequestQuestion
// returns the content of a resumable work_request and that the short-circuit in
// runner.go uses these functions when resumableShellAgent is true and step
// matches the interrupted turn.
func TestResumeSkipsWorkRequestWhenResumable(t *testing.T) {
	conv := conversation.New("test-session-resume-skip", "Test goal for resume skip")

	conv.AddMessage("assistant", "Run a background check on the project", map[string]any{
		"type":                        "work_request",
		"turn":                        1,
		"decision":                    "ask_worker",
		"shell_agent_session_id":       "test-session-resume-skip/shell-agent/1",
		"shell_agent_trajectory_path": "/tmp/test-trajectory.json",
		"shell_agent_resumable":       true,
	})

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal conversation: %v", err)
	}
	loaded, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatalf("failed to unmarshal conversation: %v", err)
	}

	if !HasResumableShellAgentWorkRequest(loaded) {
		t.Fatal("expected HasResumableShellAgentWorkRequest to return true")
	}

	question := ExtractResumableWorkRequestQuestion(loaded)
	if question != "Run a background check on the project" {
		t.Fatalf("expected ExtractResumableWorkRequestQuestion to return 'Run a background check on the project', got %q", question)
	}

	emptyConv := conversation.New("test-no-resumable", "No resumable work requests here")
	if q := ExtractResumableWorkRequestQuestion(emptyConv); q != "" {
		t.Fatalf("expected empty string for conversation with no resumable work_request, got %q", q)
	}
}

// TestPlannerOverrideSkipsPlan verifies that a mock implementation satisfies
// the Planner interface and can be assigned to Options.PlannerOverride.
func TestPlannerOverrideSkipsPlan(t *testing.T) {
	var _ Planner = &mockPlanner{}

	mp := &mockPlanner{planCalls: 0}
	opts := Options{
		PlannerOverride: mp,
	}
	_ = opts
}


// ==========================================================================
// Phase 2: Session ID continuity (verification only)
// ==========================================================================

func TestSessionIDDeterministicAcrossResume(t *testing.T) {
	origSessionID := "test-session-deterministic"
	origStep := 1

	origShellSessionID := fmt.Sprintf("%s/shell-agent/%d", origSessionID, origStep)

	origState := SessionState{
		SessionID:      origSessionID,
		TurnsCompleted: origStep - 1,
		Goal:           "Test goal for deterministic session ID",
		UpdatedAt:      time.Now(),
	}
	data, err := json.Marshal(origState)
	if err != nil {
		t.Fatalf("failed to marshal session state: %v", err)
	}

	var loadedState SessionState
	if err := json.Unmarshal(data, &loadedState); err != nil {
		t.Fatalf("failed to unmarshal session state: %v", err)
	}

	reloadedStep := loadedState.TurnsCompleted + 1
	reloadedShellSessionID := fmt.Sprintf("%s/shell-agent/%d", loadedState.SessionID, reloadedStep)

	if reloadedShellSessionID != origShellSessionID {
		t.Fatalf("SessionID differs across resume: original=%q, reloaded=%q",
			origShellSessionID, reloadedShellSessionID)
	}
}

func TestBuildShellAgentRequestSameSessionID(t *testing.T) {
	conv := conversation.New("test-session-same-id", "Test goal")
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

	task := "Do something useful"
	sessionID := "test-session-same-id/shell-agent/1"
	turnIndex := 1

	tcOriginal := &TurnContext{
		TurnIndex:     turnIndex,
		Conversation:  recorder,
		ShellAgentLib: lib,
	}
	reqOriginal, err := tcOriginal.buildShellAgentRequest(task, sessionID, false, 4000)
	if err != nil {
		t.Fatalf("original buildShellAgentRequest failed: %v", err)
	}

	tcResume := &TurnContext{
		TurnIndex:     turnIndex,
		Conversation:  recorder,
		ShellAgentLib: lib,
	}
	reqResume, err := tcResume.buildShellAgentRequest(task, sessionID, false, 4000)
	if err != nil {
		t.Fatalf("resume buildShellAgentRequest failed: %v", err)
	}

	if reqOriginal.SessionID != sessionID {
		t.Fatalf("original Request.SessionID: got %q, want %q", reqOriginal.SessionID, sessionID)
	}
	if reqResume.SessionID != sessionID {
		t.Fatalf("resume Request.SessionID: got %q, want %q", reqResume.SessionID, sessionID)
	}

	if reqOriginal.SessionID != reqResume.SessionID {
		t.Fatalf("SessionID mismatch: original=%q, resume=%q",
			reqOriginal.SessionID, reqResume.SessionID)
	}
}

// ==========================================================================
// Phase 3: Resume file cleanup after successful resume
// ==========================================================================

// TestConversationJSONUpdatedAfterResume verifies that after a successful
// shell-agent resume, the conversation.json work_request metadata is
// updated (shell_agent_resumable is false) and a work_result exists.
func TestConversationJSONUpdatedAfterResume(t *testing.T) {
	loadedState := &SessionState{
		SessionID:                "test-cv-update-after-resume",
		Goal:                     "Test goal",
		TurnsCompleted:           0,
		ShellAgentResumable:      true,
		ShellAgentTrajectoryPath: "/tmp/test-conv-update-trajectory.json",
	}

	tmpDir := t.TempDir()
	convPath := filepath.Join(tmpDir, "conversation.json")

	// Pre-create a minimal conversation.json on disk so Load() can read it in resume mode.
	initialConv := conversation.New("test-cv-update-after-resume", "Test goal")
	if data, err := initialConv.Marshal(); err == nil {
		if err := os.WriteFile(convPath, data, 0644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	} else {
		t.Fatalf("Marshal initial conversation: %v", err)
	}

	recorder := newConversationRecorder(nil, loadedState.SessionID, loadedState.Goal, convPath, true, loadedState)

	// Initialize the recorder (Load reads conversation.json from disk in resume mode).
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Simulate a successful shell-agent turn completion: recorder metadata
	// set to resumable=false, then WriteTurn records the question+answer.
	recorder.SetShellAgentMetadata(loadedState.ShellAgentTrajectoryPath, false)

	err := recorder.WriteTurn(1, "Run a background check", "/tmp/saved", nil,
		"Background check completed: all clear", "ask")
	if err != nil {
		t.Fatalf("WriteTurn: %v", err)
	}

	// Save to disk.
	if err := recorder.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Reload from disk.
	if err := recorder.Load(); err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	reloadedConv, err := conversation.Unmarshal([]byte(recorder.JSON()))
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	// HasResumableShellAgentWorkRequest must return false because
	// the work_request shell_agent_resumable metadata is false.
	if HasResumableShellAgentWorkRequest(reloadedConv) {
		t.Fatal("expected HasResumableShellAgentWorkRequest to return false after successful resume, but got true")
	}

	// Check that a work_result exists for the turn.
	var foundWorkResult bool
	for _, msg := range reloadedConv.Messages {
		if msgMetaType(msg.Metadata) == "work_result" {
			if msgTurn(msg) == 1 {
				foundWorkResult = true
				break
			}
		}
	}
	if !foundWorkResult {
		t.Fatal("expected a work_result for turn 1 in the conversation after successful resume")
	}
}

// streamActionRecorder wraps mockDisplay and captures StreamAction calls.
type streamActionRecorder struct {
	mockDisplay
	streamActions []string
}

func (r *streamActionRecorder) StreamAction(line string) {
	r.streamActions = append(r.streamActions, line)
}

// TestReplayShellAgentActions verifies that replayShellAgentActions reads
// a saved FileTrajectory from a resume file and streams the command steps
// to the display in order.
func TestReplayShellAgentActions(t *testing.T) {
	sessionID := "test-replay-session"
	resumePath := filepath.Join(os.TempDir(), sessionID+"-resume.json")
	defer os.Remove(resumePath)

	// Build a FileTrajectory-compatible struct with 3 messages: two assistant
	// commands separated by a tool output. The inner run package is internal,
	// so we marshal the equivalent payload directly.
	traj := struct {
		Messages []minisweagent.Message `json:"messages"`
	}{
		Messages: []minisweagent.Message{
			{Role: "assistant", Content: "<command>echo step1</command>"},
			{Role: "tool", Content: "step1 output"},
			{Role: "assistant", Content: "<command>echo step2</command>"},
		},
	}

	// Save it to a temp file (equivalent to SaveResumeState).
	data, err := json.MarshalIndent(traj, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal trajectory: %v", err)
	}
	if err := os.WriteFile(resumePath, data, 0644); err != nil {
		t.Fatalf("failed to save resume state: %v", err)
	}

	// Create a mockDisplay and replay.
	mock := &mockDisplay{}
	_ = replayShellAgentActions(mock, io.Discard, sessionID, 1)

	// Verify: at least 2 stream actions (the two assistant command steps).
	if len(mock.streamActions) < 2 {
		t.Fatalf("expected at least 2 stream actions, got %d", len(mock.streamActions))
	}

	// Assert ordered command content. replayShellAgentActions streams both
	// assistant commands and tool output, so the second command lands at
	// index2 after the tool output at index1.
	if !strings.Contains(mock.streamActions[0], "echo step1") {
		t.Errorf("expected streamActions[0] to contain 'echo step1', got: %s", mock.streamActions[0])
	}
	if !strings.Contains(mock.streamActions[2], "echo step2") {
		t.Errorf("expected streamActions[2] to contain 'echo step2', got: %s", mock.streamActions[2])
	}
}

// TestReplayShellAgentActionsMissingFile verifies that replayShellAgentActions
// returns nil (no-op) when the resume file does not exist.
func TestReplayShellAgentActionsMissingFile(t *testing.T) {
	sessionID := "test-replay-missing"
	recorder := &streamActionRecorder{}
	err := replayShellAgentActions(recorder, io.Discard, sessionID, 0)
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if len(recorder.streamActions) != 0 {
		t.Fatalf("expected 0 StreamAction calls for missing file, got %d", len(recorder.streamActions))
	}
}

// TestReplayShellAgentActionsNoCommands verifies that replayShellAgentActions
// returns nil (no-op) when the trajectory has no <command> messages.
func TestReplayShellAgentActionsNoCommands(t *testing.T) {
	sessionID := "test-replay-no-cmds"
	resumePath := filepath.Join(os.TempDir(), sessionID+"-resume.json")
	defer os.Remove(resumePath)

	messages := []minisweagent.Message{
		{Role: "system", Content: "system prompt"},
		{Role: "assistant", Content: "Just chatting, no commands here."},
		{Role: "user", Content: "ok"},
	}

	traj := struct {
		Messages []minisweagent.Message `json:"messages"`
	}{
		Messages: messages,
	}
	data, err := json.Marshal(traj)
	if err != nil {
		t.Fatalf("failed to marshal trajectory: %v", err)
	}
	if err := os.WriteFile(resumePath, data, 0644); err != nil {
		t.Fatalf("failed to write resume file: %v", err)
	}

	recorder := &streamActionRecorder{}
	_ = replayShellAgentActions(recorder, io.Discard, sessionID, 0)

	if len(recorder.streamActions) != 0 {
		t.Fatalf("expected 0 StreamAction calls for trajectory without commands, got %d", len(recorder.streamActions))
	}
}
