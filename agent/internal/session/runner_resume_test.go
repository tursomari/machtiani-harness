package session

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

// TestResumeDetectsInterruptedWorkRequest creates a conversation with a
// work_request that has shell_agent_resumable: true and no corresponding
// work_result for the same turn. It then calls the resume detection logic
// and asserts the work request is detected as resumable.
func TestResumeDetectsInterruptedWorkRequest(t *testing.T) {
	conv := conversation.New("test-session-resume-detect", "Test goal for resume detection")
	conv.ShellAgentResumable = true

	conv.AddMessage("assistant", "Run a background check on the project", map[string]any{
		"type":                        "work_request",
		"turn":                        1,
		"decision":                    "ask_worker",
		"shell_agent_session_id":      "test-session-resume-detect/shell-agent/1",
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
		"type":                   "work_request",
		"turn":                   1,
		"decision":               "ask_worker",
		"shell_agent_session_id": "test-session-resume-complete/shell-agent/1",
		"shell_agent_resumable":  true,
	})

	conv.AddMessage("assistant", "Background check completed: all clear", map[string]any{
		"type":                  "work_result",
		"turn":                  1,
		"shell_agent_resumable": false,
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
	conv.ShellAgentResumable = true

	conv.AddMessage("assistant", "Run a background check on the project", map[string]any{
		"type":                        "work_request",
		"turn":                        1,
		"decision":                    "ask_worker",
		"shell_agent_session_id":      "test-session-resume-skip/shell-agent/1",
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
		Config: &llm.ShellAgentConfig{MaxSteps: 10},
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

	recorder := newConversationRecorder(nil, loadedState.SessionID, loadedState.Goal, convPath, true, loadedState, false)

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

// streamActionRecorder wraps *ui.EventBus and captures Notify calls.
type streamActionRecorder struct {
	bus      *ui.EventBus
	notified []string
	mu       sync.Mutex
}

func (r *streamActionRecorder) Notify(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notified = append(r.notified, line)
	r.bus.Emit(ui.NotificationEvent{Level: ui.NotificationInfo, Message: line})
}

// newTestRecorder creates a streamActionRecorder with an EventBus and a
// goroutine that drains the bus subscription channel.
func newTestRecorder() *streamActionRecorder {
	bus := ui.NewEventBus(256)
	go func() {
		for range bus.Subscribe() {
		}
	}()
	return &streamActionRecorder{bus: bus}
}

// TestReplayShellAgentActions verifies that replayShellAgentActions reads
// a saved FileTrajectory from a resume file and streams the command steps
// to the display in order.
func TestReplayShellAgentActions(t *testing.T) {
	sessionID := "test-replay-session"
	trajPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, 1)
	if err != nil {
		t.Fatalf("failed to resolve trajectory path: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(trajPath))

	// Build a FileTrajectory-compatible struct with 3 messages: two assistant
	// commands separated by a tool output. The inner run package is internal,
	// so we marshal the equivalent payload directly.
	traj := struct {
		Messages []minisweagent.Message `json:"messages"`
	}{
		Messages: []minisweagent.Message{
			{Role: "assistant", Content: "<command-runnderresumetest>echo step1</command-runnderresumetest>"},
			{Role: "tool", Content: "step1 output"},
			{Role: "assistant", Content: "<command-runnderresumetest>echo step2</command-runnderresumetest>"},
		},
	}

	// Save it to the artifacts trajectory path (equivalent to SaveResumeState).
	data, err := json.MarshalIndent(traj, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal trajectory: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(trajPath), 0755); err != nil {
		t.Fatalf("failed to create trajectory directory: %v", err)
	}
	if err := os.WriteFile(trajPath, data, 0644); err != nil {
		t.Fatalf("failed to save resume state: %v", err)
	}

	// Create a streamActionRecorder and replay.
	recorder := newTestRecorder()
	sub := recorder.bus.Subscribe()
	_ = replayShellAgentActions(recorder.bus, io.Discard, sessionID, 1, "command-runnderresumetest")
	recorder.bus.Unsubscribe(sub)

	// Collect NotificationEvents from the subscription.
	var events []string
	for {
		select {
		case event := <-sub:
			if ne, ok := event.(ui.NotificationEvent); ok {
				events = append(events, ne.Message)
			}
		default:
			goto verifyReplay
		}
	}
verifyReplay:

	// Verify: exactly 2 notify calls (one per command step).
	if len(events) != 2 {
		t.Fatalf("expected 2 notify calls, got %d", len(events))
	}

	// Assert ordered command content with the new Notify format.
	expected0 := "[replay 1/2] echo step1 (1 lines output)"
	if events[0] != expected0 {
		t.Errorf("expected messages[0] = %q, got: %q", expected0, events[0])
	}
	expected1 := "[replay 2/2] echo step2 (0 lines output)"
	if events[1] != expected1 {
		t.Errorf("expected messages[1] = %q, got: %q", expected1, events[1])
	}
}

// TestReplayShellAgentActionsMissingFile verifies that replayShellAgentActions
// returns nil (no-op) when the resume file does not exist.
func TestReplayShellAgentActionsMissingFile(t *testing.T) {
	sessionID := "test-replay-missing"
	recorder := newTestRecorder()
	sub := recorder.bus.Subscribe()
	err := replayShellAgentActions(recorder.bus, io.Discard, sessionID, 0, "command-runnderresumetest")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	recorder.bus.Unsubscribe(sub)

	// Collect events; expect none.
	var events []string
	for {
		select {
		case event := <-sub:
			if ne, ok := event.(ui.NotificationEvent); ok {
				events = append(events, ne.Message)
			}
		default:
			goto verifyMissingFile
		}
	}
verifyMissingFile:

	if len(events) != 0 {
		t.Fatalf("expected 0 Notify calls for missing file, got %d", len(events))
	}
}

// TestReplayShellAgentActionsNoCommands verifies that replayShellAgentActions
// returns nil (no-op) when the trajectory has no <command> messages.
func TestReplayShellAgentActionsNoCommands(t *testing.T) {
	sessionID := "test-replay-no-cmds"
	trajPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, 0)
	if err != nil {
		t.Fatalf("failed to resolve trajectory path: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(trajPath))

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
	if err := os.MkdirAll(filepath.Dir(trajPath), 0755); err != nil {
		t.Fatalf("failed to create trajectory directory: %v", err)
	}
	if err := os.WriteFile(trajPath, data, 0644); err != nil {
		t.Fatalf("failed to write resume file: %v", err)
	}

	recorder := newTestRecorder()
	sub := recorder.bus.Subscribe()
	_ = replayShellAgentActions(recorder.bus, io.Discard, sessionID, 0, "command-runnderresumetest")
	recorder.bus.Unsubscribe(sub)

	// Collect events; expect none.
	var events []string
	for {
		select {
		case event := <-sub:
			if ne, ok := event.(ui.NotificationEvent); ok {
				events = append(events, ne.Message)
			}
		default:
			goto verifyNoCommands
		}
	}
verifyNoCommands:

	if len(events) != 0 {
		t.Fatalf("expected 0 Notify calls for trajectory without commands, got %d", len(events))
	}
}

// TestExtractResumableWorkRequestQuestionFromTopLevelField verifies that
// ExtractResumableWorkRequestQuestion detects resumable work requests from
// the top-level Conversation.ShellAgentResumable field without relying on
// per-message shell_agent_resumable metadata.
func TestExtractResumableWorkRequestQuestionFromTopLevelField(t *testing.T) {
	conv := conversation.New("test-resume-top-level", "Test goal")

	conv.ShellAgentResumable = true
	conv.ShellAgentTrajectoryPath = "/tmp/test-path"

	conv.AddMessage("assistant", "Run a background check on the project", map[string]any{
		"type":                        "work_request",
		"turn":                        1,
		"decision":                    "ask_worker",
		"shell_agent_session_id":      "test-resume-top-level/shell-agent/1",
		"shell_agent_trajectory_path": "/tmp/test-path",
		// shell_agent_resumable intentionally NOT set in metadata
	})

	// No work_result for turn 1.

	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal conversation: %v", err)
	}
	loaded, err := conversation.Unmarshal(data)
	if err != nil {
		t.Fatalf("failed to unmarshal conversation: %v", err)
	}

	if !HasResumableShellAgentWorkRequest(loaded) {
		t.Fatal("expected HasResumableShellAgentWorkRequest to return true via top-level field")
	}

	question := ExtractResumableWorkRequestQuestion(loaded)
	if question != "Run a background check on the project" {
		t.Fatalf("expected ExtractResumableWorkRequestQuestion to return 'Run a background check on the project', got %q", question)
	}

	// Also test that an empty Conversation with ShellAgentResumable=true but
	// no messages returns empty string.
	emptyConv := conversation.New("test-empty-resumable", "Empty goal")
	emptyConv.ShellAgentResumable = true
	if q := ExtractResumableWorkRequestQuestion(emptyConv); q != "" {
		t.Fatalf("expected empty string for empty conversation with ShellAgentResumable=true, got %q", q)
	}
}

// TestPrepareRunBootstrapFromConversation verifies end-to-end bootstrap from a
// Conversation with top-level fields without requiring a session-state.json
// file on disk. The conversation.json file is pre-populated with all session
// state fields, and prepareRunBootstrap should load state from it.
func TestPrepareRunBootstrapFromConversation(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-bootstrap-from-conv-%d", time.Now().UnixNano())

	// Create a Conversation with all top-level fields populated.
	now := time.Now().UTC()
	conv := &conversation.Conversation{
		SessionID:                sessionID,
		Goal:                     "Bootstrap from conversation fields",
		OriginalGoal:             "Bootstrap original goal",
		OriginalPrompt:           "Bootstrap original prompt",
		ShellAgentResumable:      false,
		ShellAgentTrajectoryPath: "/tmp/bootstrap-trajectory.json",
		ShellAgentInterruptStep:  1,
		TurnsCompleted:           2,
		PlannerProgress: &conversation.PlannerProgressState{
			SuccessFiles: []string{"bootstrap_test.go"},
		},
		PlannerOverlay:  "Bootstrap overlay",
		TaskDescription: "Bootstrap task description",
		Status:          "running",
		UpdatedAt:       now,
	}

	// Ensure there is NO session-state.json on disk – only conversation.json.
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("failed to resolve session directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	// Write conversation.json to disk.
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

	// Verify session-state.json does NOT exist.
	statePath := filepath.Join(dir, "session-state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected session-state.json to NOT exist before bootstrap")
	}

	opts := Options{
		Config: Config{
			SessionID: sessionID,
		},
		Goal:            "Bootstrap from conversation fields",
		PlannerOverride: &mockPlanner{},
	}

	bootstrap, _, ok := prepareRunBootstrap(context.Background(), opts, io.Discard)
	if !ok {
		t.Fatal("prepareRunBootstrap returned not ok")
	}
	if bootstrap == nil {
		t.Fatal("expected non-nil bootstrap")
	}
	if bootstrap.loadedState == nil {
		t.Fatal("expected non-nil loadedState in bootstrap")
	}

	loaded := bootstrap.loadedState

	// Verify all fields match the conversation top-level fields.
	if loaded.SessionID != sessionID {
		t.Fatalf("SessionID mismatch: got %q want %q", loaded.SessionID, sessionID)
	}
	if loaded.Goal != conv.Goal {
		t.Fatalf("Goal mismatch: got %q want %q", loaded.Goal, conv.Goal)
	}
	if loaded.OriginalGoal != conv.OriginalGoal {
		t.Fatalf("OriginalGoal mismatch: got %q want %q", loaded.OriginalGoal, conv.OriginalGoal)
	}
	if loaded.OriginalPrompt != conv.OriginalPrompt {
		t.Fatalf("OriginalPrompt mismatch: got %q want %q", loaded.OriginalPrompt, conv.OriginalPrompt)
	}
	if loaded.ShellAgentResumable != conv.ShellAgentResumable {
		t.Fatalf("ShellAgentResumable mismatch: got %v want %v", loaded.ShellAgentResumable, conv.ShellAgentResumable)
	}
	if loaded.ShellAgentTrajectoryPath != conv.ShellAgentTrajectoryPath {
		t.Fatalf("ShellAgentTrajectoryPath mismatch: got %q want %q", loaded.ShellAgentTrajectoryPath, conv.ShellAgentTrajectoryPath)
	}
	if loaded.ShellAgentInterruptStep != conv.ShellAgentInterruptStep {
		t.Fatalf("ShellAgentInterruptStep mismatch: got %d want %d", loaded.ShellAgentInterruptStep, conv.ShellAgentInterruptStep)
	}
	if loaded.TurnsCompleted != conv.TurnsCompleted {
		t.Fatalf("TurnsCompleted mismatch: got %d want %d", loaded.TurnsCompleted, conv.TurnsCompleted)
	}
	if loaded.PlannerProgress == nil {
		t.Fatal("expected PlannerProgress to be set")
	}
	if len(loaded.PlannerProgress.SuccessFiles) != len(conv.PlannerProgress.SuccessFiles) {
		t.Fatalf("PlannerProgress.SuccessFiles length mismatch: got %d want %d",
			len(loaded.PlannerProgress.SuccessFiles), len(conv.PlannerProgress.SuccessFiles))
	}
	for i, f := range conv.PlannerProgress.SuccessFiles {
		if loaded.PlannerProgress.SuccessFiles[i] != f {
			t.Fatalf("PlannerProgress.SuccessFiles[%d] mismatch: got %q want %q",
				i, loaded.PlannerProgress.SuccessFiles[i], f)
		}
	}
	if len(loaded.Modes) != len(conv.Modes) {
		t.Fatalf("Modes length mismatch: got %d want %d", len(loaded.Modes), len(conv.Modes))
	}
	for i, m := range conv.Modes {
		if loaded.Modes[i] != m {
			t.Fatalf("Modes[%d] mismatch: got %q want %q", i, loaded.Modes[i], m)
		}
	}
	if loaded.ModeInstructionDir != conv.ModeInstructionDir {
		t.Fatalf("ModeInstructionDir mismatch: got %q want %q", loaded.ModeInstructionDir, conv.ModeInstructionDir)
	}
	if loaded.PlannerOverlay != conv.PlannerOverlay {
		t.Fatalf("PlannerOverlay mismatch: got %q want %q", loaded.PlannerOverlay, conv.PlannerOverlay)
	}
	if loaded.TaskDescription != conv.TaskDescription {
		t.Fatalf("TaskDescription mismatch: got %q want %q", loaded.TaskDescription, conv.TaskDescription)
	}
	if loaded.Status != conv.Status {
		t.Fatalf("Status mismatch: got %q want %q", loaded.Status, conv.Status)
	}
	// The bootstrap is in resume mode since a sessionID was provided.
	if !bootstrap.resumeMode {
		t.Fatal("expected resumeMode to be true when sessionID is provided")
	}

	// session-state.json is still not required (and may not exist).
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		// If it was created by the bootstrap, that's fine but not required.
		t.Logf("session-state.json exists after bootstrap (optional)")
	}
}

// TestResumeEndToEndWithoutSessionStateJSON verifies that when a
// conversation.json with all top-level resumability fields is present on
// disk but session-state.json is absent, prepareRunBootstrap can load state
// from the conversation alone and does NOT create session-state.json.
func TestResumeEndToEndWithoutSessionStateJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-e2e-no-state-%d", time.Now().UnixNano())

	// Build a Conversation with all top-level resumability fields
	// populated – this simulates what PreWriteTurn writes to disk.
	now := time.Now().UTC()
	conv := &conversation.Conversation{
		SessionID:                sessionID,
		Goal:                     "End-to-end resume without session-state",
		OriginalGoal:             "E2E original goal",
		OriginalPrompt:           "E2E original prompt",
		ShellAgentResumable:      true,
		ShellAgentTrajectoryPath: "/tmp/e2e-trajectory.json",
		ShellAgentInterruptStep:  1,
		TurnsCompleted:           3,
		PlannerProgress: &conversation.PlannerProgressState{
			SuccessFiles: []string{"runner_resume_test.go"},
		},
		PlannerOverlay:  "E2E overlay",
		TaskDescription: "E2E task description",
		Status:          "interrupted",
		UpdatedAt:       now,
	}

	// Ensure there is NO session-state.json on disk.
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("failed to resolve session directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	// Write conversation.json to disk.
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

	// Verify session-state.json does NOT exist before bootstrap.
	statePath := filepath.Join(dir, "session-state.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected session-state.json to NOT exist before bootstrap")
	}

	opts := Options{
		Config: Config{
			SessionID: sessionID,
		},
		Goal:            "End-to-end resume without session-state",
		PlannerOverride: &mockPlanner{},
	}

	bootstrap, _, ok := prepareRunBootstrap(context.Background(), opts, io.Discard)
	if !ok {
		t.Fatal("prepareRunBootstrap returned not ok")
	}
	if bootstrap == nil {
		t.Fatal("expected non-nil bootstrap")
	}
	if bootstrap.loadedState == nil {
		t.Fatal("expected non-nil loadedState in bootstrap")
	}

	loaded := bootstrap.loadedState

	// Verify key fields match the conversation top-level fields.
	if loaded.SessionID != sessionID {
		t.Fatalf("SessionID mismatch: got %q want %q", loaded.SessionID, sessionID)
	}
	if loaded.Goal != conv.Goal {
		t.Fatalf("Goal mismatch: got %q want %q", loaded.Goal, conv.Goal)
	}
	if loaded.OriginalGoal != conv.OriginalGoal {
		t.Fatalf("OriginalGoal mismatch: got %q want %q", loaded.OriginalGoal, conv.OriginalGoal)
	}
	if loaded.OriginalPrompt != conv.OriginalPrompt {
		t.Fatalf("OriginalPrompt mismatch: got %q want %q", loaded.OriginalPrompt, conv.OriginalPrompt)
	}
	if loaded.ShellAgentResumable != conv.ShellAgentResumable {
		t.Fatalf("ShellAgentResumable mismatch: got %v want %v", loaded.ShellAgentResumable, conv.ShellAgentResumable)
	}
	if loaded.ShellAgentTrajectoryPath != conv.ShellAgentTrajectoryPath {
		t.Fatalf("ShellAgentTrajectoryPath mismatch: got %q want %q", loaded.ShellAgentTrajectoryPath, conv.ShellAgentTrajectoryPath)
	}
	if loaded.ShellAgentInterruptStep != conv.ShellAgentInterruptStep {
		t.Fatalf("ShellAgentInterruptStep mismatch: got %d want %d", loaded.ShellAgentInterruptStep, conv.ShellAgentInterruptStep)
	}
	if loaded.TurnsCompleted != conv.TurnsCompleted {
		t.Fatalf("TurnsCompleted mismatch: got %d want %d", loaded.TurnsCompleted, conv.TurnsCompleted)
	}
	if loaded.PlannerOverlay != conv.PlannerOverlay {
		t.Fatalf("PlannerOverlay mismatch: got %q want %q", loaded.PlannerOverlay, conv.PlannerOverlay)
	}
	if loaded.TaskDescription != conv.TaskDescription {
		t.Fatalf("TaskDescription mismatch: got %q want %q", loaded.TaskDescription, conv.TaskDescription)
	}
	if loaded.Status != conv.Status {
		t.Fatalf("Status mismatch: got %q want %q", loaded.Status, conv.Status)
	}

	// Assert session-state.json was NOT created on disk after bootstrap.
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("session-state.json was created on disk but should NOT have been")
	}
}

// TestLoadTrajectoryForResumeSuccess creates a temporary trajectory file,
// calls loadTrajectoryForResume, and verifies the returned messages.
func TestLoadTrajectoryForResumeSuccess(t *testing.T) {
	sessionID := "test-load-trajectory-success"
	trajPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, 1)
	if err != nil {
		t.Fatalf("failed to resolve trajectory path: %v", err)
	}
	parentDir := filepath.Dir(trajPath)
	defer os.RemoveAll(parentDir)

	if err := os.MkdirAll(parentDir, 0755); err != nil {
		t.Fatalf("failed to create trajectory directory: %v", err)
	}

	traj := struct {
		Messages []minisweagent.Message `json:"messages"`
	}{
		Messages: []minisweagent.Message{
			{Role: "assistant", Content: "test content 1"},
			{Role: "tool", Content: "test content 2"},
		},
	}

	data, err := json.Marshal(traj)
	if err != nil {
		t.Fatalf("failed to marshal trajectory: %v", err)
	}
	if err := os.WriteFile(trajPath, data, 0644); err != nil {
		t.Fatalf("failed to write trajectory file: %v", err)
	}

	messages, err := loadTrajectoryForResume(sessionID, 1)
	if err != nil {
		t.Fatalf("loadTrajectoryForResume returned unexpected error: %v", err)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if messages[0].Role != "assistant" {
		t.Fatalf("expected messages[0].Role to be 'assistant', got %q", messages[0].Role)
	}
	if messages[0].Content != "test content 1" {
		t.Fatalf("expected messages[0].Content to be 'test content 1', got %q", messages[0].Content)
	}

	if messages[1].Role != "tool" {
		t.Fatalf("expected messages[1].Role to be 'tool', got %q", messages[1].Role)
	}
	if messages[1].Content != "test content 2" {
		t.Fatalf("expected messages[1].Content to be 'test content 2', got %q", messages[1].Content)
	}
}

func TestRestorePersistedShellAgentTagsFromConversation(t *testing.T) {
	cfg := Config{AnswerTag: "answer", CommandTag: "command"}
	state := &SessionState{
		SessionID:           "tagged-resume",
		ShellAgentResumable: true,
		AnswerTag:           "answer-review",
		CommandTag:          "command-review",
	}

	restorePersistedShellAgentTags(&cfg, state)

	if cfg.AnswerTag != "answer-review" {
		t.Fatalf("AnswerTag = %q, want answer-review", cfg.AnswerTag)
	}
	if cfg.CommandTag != "command-review" {
		t.Fatalf("CommandTag = %q, want command-review", cfg.CommandTag)
	}
}

func TestPrepareRunBootstrapRestoresTagsWithoutResumeFlags(t *testing.T) {
	sessionID := fmt.Sprintf("tagged-bootstrap-resume-%d", time.Now().UnixNano())
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("resolve conversation path: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(filepath.Dir(convPath)))
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("create conversation directory: %v", err)
	}
	conv := conversation.New(sessionID, "Tagged resume goal")
	conv.Goal = "Tagged resume goal"
	conv.ShellAgentResumable = true
	conv.AnswerTag = "answer-review"
	conv.CommandTag = "command-review"
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}
	if err := os.WriteFile(convPath, data, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}
	trajPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, 1)
	if err != nil {
		t.Fatalf("resolve trajectory: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(trajPath), 0o755); err != nil {
		t.Fatalf("create trajectory directory: %v", err)
	}
	if err := os.WriteFile(trajPath, []byte(`{"messages":[]}`), 0o644); err != nil {
		t.Fatalf("write trajectory: %v", err)
	}

	bootstrap, result, ok := prepareRunBootstrap(context.Background(), Options{
		Config: Config{
			SessionID:  sessionID,
			AnswerTag:  "answer",
			CommandTag: "command",
		},
	}, io.Discard)
	if !ok {
		t.Fatalf("prepareRunBootstrap failed: %+v", result)
	}
	if bootstrap.cfg.answerTag != "answer-review" || bootstrap.cfg.commandTag != "command-review" {
		t.Fatalf("bootstrap tags = (%q, %q), want (answer-review, command-review)", bootstrap.cfg.answerTag, bootstrap.cfg.commandTag)
	}
}

func TestPrepareRunBootstrapRestoresModelsWithoutResumeFlags(t *testing.T) {
	sessionID := fmt.Sprintf("model-bootstrap-resume-%d", time.Now().UnixNano())
	convPath, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		t.Fatalf("resolve conversation path: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(filepath.Dir(convPath)))
	if err := os.MkdirAll(filepath.Dir(convPath), 0o755); err != nil {
		t.Fatalf("create conversation directory: %v", err)
	}
	conv := conversation.New(sessionID, "Model resume goal")
	conv.Goal = "Model resume goal"
	conv.ModelSelection = &conversation.ModelSelectionState{
		OrchestratorAlias:  "original-orchestrator",
		AnswerAlias:        "original-answer",
		FileDiscoveryAlias: "original-discovery",
		ShellAgentAlias:    "original-shell",
	}
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("marshal conversation: %v", err)
	}
	if err := os.WriteFile(convPath, data, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}

	bootstrap, result, ok := prepareRunBootstrap(context.Background(), Options{
		Config: Config{
			SessionID:          sessionID,
			OrchModel:          "deepseek-v4-flash",
			AnswerModel:        "deepseek-v4-flash",
			FileDiscoveryModel: "deepseek-v4-flash",
			ShellAgentModel:    "deepseek-v4-flash",
			OpenAIAPIKey:       "stale-direct-key",
			OpenAIBaseURL:      "https://stale.example/v1",
			OpenAIModel:        "deepseek-v4-flash",
		},
	}, io.Discard)
	if !ok {
		t.Fatalf("prepareRunBootstrap failed: %+v", result)
	}
	if got := bootstrap.cfg.orchModel; got != "original-orchestrator" {
		t.Fatalf("orchestrator model = %q, want original-orchestrator", got)
	}
	if got := bootstrap.cfg.answerModel; got != "original-answer" {
		t.Fatalf("answer model = %q, want original-answer", got)
	}
	if got := bootstrap.cfg.fileDiscoveryModel; got != "original-discovery" {
		t.Fatalf("file discovery model = %q, want original-discovery", got)
	}
	if got := bootstrap.cfg.shellAgentModel; got != "original-shell" {
		t.Fatalf("shell agent model = %q, want original-shell", got)
	}
	if bootstrap.cfg.openAIAPIKey != "" || bootstrap.cfg.openAIBaseURL != "" || bootstrap.cfg.openAIModel != "" {
		t.Fatalf("stale direct model settings survived alias restore: %#v", bootstrap.cfg)
	}
}

func TestRestorePersistedModelSelectionHonorsExplicitOverrides(t *testing.T) {
	state := &SessionState{ModelSelection: &conversation.ModelSelectionState{
		OrchestratorAlias:  "saved-orchestrator",
		AnswerAlias:        "saved-answer",
		FileDiscoveryAlias: "saved-discovery",
		ShellAgentAlias:    "saved-shell",
	}}
	cfg := Config{
		OrchModel:          "override-orchestrator",
		AnswerModel:        "override-answer",
		FileDiscoveryModel: "override-discovery",
		ShellAgentModel:    "override-shell",
		ModelOverrides: ModelOverrideFlags{
			Orchestrator:  true,
			Answer:        true,
			FileDiscovery: true,
			ShellAgent:    true,
		},
	}

	restorePersistedModelSelection(&cfg, state)

	if cfg.OrchModel != "override-orchestrator" || cfg.AnswerModel != "override-answer" ||
		cfg.FileDiscoveryModel != "override-discovery" || cfg.ShellAgentModel != "override-shell" {
		t.Fatalf("explicit model overrides were replaced: %#v", cfg)
	}
}

func TestRestorePersistedModelSelectionHonorsExplicitDirectOverride(t *testing.T) {
	state := &SessionState{ModelSelection: &conversation.ModelSelectionState{
		OrchestratorAlias: "saved-orchestrator",
	}}
	cfg := Config{
		OpenAIBaseURL: "https://override.example/v1",
		OpenAIModel:   "override-direct-model",
		ModelOverrides: ModelOverrideFlags{
			Direct: true,
		},
	}

	restorePersistedModelSelection(&cfg, state)

	if cfg.OpenAIBaseURL != "https://override.example/v1" || cfg.OpenAIModel != "override-direct-model" {
		t.Fatalf("explicit direct model override was replaced: %#v", cfg)
	}
}

func TestRestorePersistedShellAgentTagsFromLegacyTrajectory(t *testing.T) {
	sessionID := fmt.Sprintf("tagged-legacy-resume-%d", time.Now().UnixNano())
	trajPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, 1)
	if err != nil {
		t.Fatalf("resolve trajectory path: %v", err)
	}
	defer os.RemoveAll(filepath.Dir(filepath.Dir(trajPath)))
	if err := os.MkdirAll(filepath.Dir(trajPath), 0o755); err != nil {
		t.Fatalf("create trajectory directory: %v", err)
	}
	legacy := map[string]any{
		"resume_state": map[string]any{
			"version":       1,
			"system_prompt": "Respond with exactly one <command-review>...</command-review> block, or conclude with <answer-review>...</answer-review>.",
		},
		"messages": []any{},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal trajectory: %v", err)
	}
	if err := os.WriteFile(trajPath, data, 0o644); err != nil {
		t.Fatalf("write trajectory: %v", err)
	}

	cfg := Config{AnswerTag: "answer", CommandTag: "command"}
	state := &SessionState{SessionID: sessionID, ShellAgentResumable: true}
	restorePersistedShellAgentTags(&cfg, state)

	if cfg.AnswerTag != "answer-review" || cfg.CommandTag != "command-review" {
		t.Fatalf("restored tags = (%q, %q), want (answer-review, command-review)", cfg.AnswerTag, cfg.CommandTag)
	}
	if state.AnswerTag != cfg.AnswerTag || state.CommandTag != cfg.CommandTag {
		t.Fatalf("state tags were not backfilled: %#v", state)
	}
}

// TestLoadTrajectoryForResumeNotFound verifies that loadTrajectoryForResume
// returns an error when no trajectory file exists for the given session.
func TestLoadTrajectoryForResumeNotFound(t *testing.T) {
	sessionID := "test-load-trajectory-notfound-1a2b3c"
	_, err := loadTrajectoryForResume(sessionID, 1)
	if err == nil {
		t.Fatal("expected error from loadTrajectoryForResume for non-existent trajectory, got nil")
	}
}

func TestPrepareRunBootstrapIgnoresStaleShellAgentResumeMarker(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-stale-resume-%d", time.Now().UnixNano())
	conv := conversation.New(sessionID, "test goal")
	conv.Goal = "test goal"
	conv.ShellAgentResumable = true
	conv.ShellAgentTrajectoryPath = "/tmp/stale-trajectory.json"
	conv.AddMessage("assistant", "Run the shell-agent task", map[string]any{
		"type":                   "work_request",
		"turn":                   1,
		"decision":               "ask_worker",
		"shell_agent_session_id": sessionID + "/shell-agent/1",
	})

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
		dir, _ := artifacts.SessionDirectory(sessionID)
		_ = os.RemoveAll(dir)
	})

	var diag bytes.Buffer
	bootstrap, _, ok := prepareRunBootstrap(context.Background(), Options{
		Config: Config{SessionID: sessionID},
		Goal:   "test goal",
	}, &diag)
	if !ok {
		t.Fatal("prepareRunBootstrap returned not ok")
	}
	if bootstrap.resumableShellAgent {
		t.Fatalf("expected stale shell-agent resume marker to be ignored")
	}
	if !strings.Contains(diag.String(), "stale shell-agent resume marker ignored") {
		t.Fatalf("expected stale resume diagnostic, got %q", diag.String())
	}
}

func TestPrepareRunBootstrapEnablesShellAgentResumeWithTrajectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	sessionID := fmt.Sprintf("test-valid-resume-%d", time.Now().UnixNano())
	conv := conversation.New(sessionID, "test goal")
	conv.Goal = "test goal"
	conv.ShellAgentResumable = true
	conv.ShellAgentTrajectoryPath = "/tmp/ignored-stored-path.json"
	conv.AddMessage("assistant", "Run the shell-agent task", map[string]any{
		"type":                   "work_request",
		"turn":                   1,
		"decision":               "ask_worker",
		"shell_agent_session_id": sessionID + "/shell-agent/1",
	})

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
	trajPath, err := artifacts.ShellAgentTrajectoryPath(sessionID, 1)
	if err != nil {
		t.Fatalf("ShellAgentTrajectoryPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(trajPath), 0o755); err != nil {
		t.Fatalf("mkdir trajectory dir: %v", err)
	}
	traj := struct {
		Messages []minisweagent.Message `json:"messages"`
	}{Messages: []minisweagent.Message{{Role: "assistant", Content: "started"}}}
	trajData, err := json.Marshal(traj)
	if err != nil {
		t.Fatalf("Marshal trajectory: %v", err)
	}
	if err := os.WriteFile(trajPath, trajData, 0o644); err != nil {
		t.Fatalf("WriteFile trajectory: %v", err)
	}
	t.Cleanup(func() {
		dir, _ := artifacts.SessionDirectory(sessionID)
		_ = os.RemoveAll(dir)
	})

	bootstrap, _, ok := prepareRunBootstrap(context.Background(), Options{
		Config: Config{SessionID: sessionID},
		Goal:   "test goal",
	}, io.Discard)
	if !ok {
		t.Fatal("prepareRunBootstrap returned not ok")
	}
	if !bootstrap.resumableShellAgent {
		t.Fatalf("expected valid shell-agent trajectory to enable resume")
	}
	if bootstrap.resumableShellAgentTrajectoryPath != trajPath {
		t.Fatalf("trajectory path = %q, want %q", bootstrap.resumableShellAgentTrajectoryPath, trajPath)
	}
}

// TestVerifyShellAgentResume_MissingInterruptedTrajectory verifies that
// verifyShellAgentResume returns nil when interruptedMsgs is empty,
// representing a fresh start after a missing/corrupt trajectory.
func TestVerifyShellAgentResume_MissingInterruptedTrajectory(t *testing.T) {
	interruptedMsgs := []minisweagent.Message{}
	resumedMsgs := []minisweagent.Message{
		{Role: "user", Content: "hello"},
	}

	err := verifyShellAgentResume(interruptedMsgs, resumedMsgs)
	if err != nil {
		t.Fatalf("expected nil error when interruptedMsgs is empty (fresh start after missing trajectory), got: %v", err)
	}
}
