package session

import (
	"bytes"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func TestApplyModeCompletedPlanWithResumePromptReturnsHandledFalse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-completed-resume"
	plan := modePlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []modeTaskState{{Title: "Implement solution", Mode: "coding", Status: "complete"}},
	}
	if err := persistModePlan(sessionID, plan); err != nil {
		t.Fatalf("persistModePlan error: %v", err)
	}

	ctx := modeContext{
		SessionID:    sessionID,
		Goal:         "fix bug",
		ResumePrompt: "add more tests",
		Config:       legacyConfig{mode: "coding"},
		Display:      ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	result, handled := applyMode(&ctx)
	if handled {
		t.Fatalf("expected handled=false for completed plan with resume prompt, got handled=%v", handled)
	}
	if len(result.Plan.Tasks) == 0 {
		t.Fatalf("expected non-empty plan tasks")
	}
}

func TestApplyModeCompletedPlanWithoutResumePromptReturnsHandledFalse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-completed-no-resume"
	plan := modePlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []modeTaskState{{Title: "Implement solution", Mode: "coding", Status: "complete"}},
	}
	if err := persistModePlan(sessionID, plan); err != nil {
		t.Fatalf("persistModePlan error: %v", err)
	}

	ctx := modeContext{
		SessionID:    sessionID,
		Goal:         "fix bug",
		ResumePrompt: "",
		Config:       legacyConfig{mode: "coding"},
		Display:      ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	// With the single-session model, completed plan without resume prompt
	// returns handled=false (the planner loop handles finalization).
	_, handled := applyMode(&ctx)
	if handled {
		t.Fatalf("expected handled=false for completed plan without resume prompt, got handled=%v", handled)
	}
}

func TestApplyModePendingTaskReturnsHandledFalse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-pending-task"
	plan := modePlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []modeTaskState{{Title: "Implement solution", Mode: "coding", Status: "pending"}},
	}
	if err := persistModePlan(sessionID, plan); err != nil {
		t.Fatalf("persistModePlan error: %v", err)
	}

	ctx := modeContext{
		SessionID: sessionID,
		Goal:      "fix bug",
		Config:    legacyConfig{mode: "coding"},
		Options:   Options{},
		Display:   ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	result, handled := applyMode(&ctx)
	if handled {
		t.Fatalf("expected handled=false for pending task, got handled=%v", handled)
	}
	if len(result.Plan.Tasks) == 0 {
		t.Fatalf("expected non-empty plan tasks")
	}
	if result.Plan.Tasks[0].Status != "running" {
		t.Fatalf("expected task status to be 'running', got %q", result.Plan.Tasks[0].Status)
	}
}

func TestApplyModeSuspendedTaskReturnsHandledFalse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-suspended-task"
	plan := modePlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []modeTaskState{{Title: "Implement solution", Mode: "coding", Status: "suspended"}},
	}
	if err := persistModePlan(sessionID, plan); err != nil {
		t.Fatalf("persistModePlan error: %v", err)
	}

	ctx := modeContext{
		SessionID:    sessionID,
		Goal:         "fix bug",
		ResumePrompt: "the answer is 42",
		Config:       legacyConfig{mode: "coding"},
		Options:      Options{},
		Display:      ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	result, handled := applyMode(&ctx)
	if handled {
		t.Fatalf("expected handled=false for suspended task with resume prompt, got handled=%v", handled)
	}
	if result.Plan.Tasks[0].Status != "running" {
		t.Fatalf("expected task status to be 'running' after resume, got %q", result.Plan.Tasks[0].Status)
	}
}

func TestApplyModeSuspendedTaskWithoutResumePromptResetsToRunning(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-suspended-no-prompt"
	plan := modePlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []modeTaskState{{Title: "Implement solution", Mode: "coding", Status: "suspended"}},
	}
	if err := persistModePlan(sessionID, plan); err != nil {
		t.Fatalf("persistModePlan error: %v", err)
	}

	ctx := modeContext{
		SessionID: sessionID,
		Goal:      "fix bug",
		Config:    legacyConfig{mode: "coding"},
		Options:   Options{},
		Display:   ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	result, handled := applyMode(&ctx)
	if handled {
		t.Fatalf("expected handled=false for suspended task without resume prompt, got handled=%v", handled)
	}
	if result.Plan.Tasks[0].Status != "running" {
		t.Fatalf("expected task status to be 'running' even without resume prompt, got %q", result.Plan.Tasks[0].Status)
	}
}

func TestCompleteModePlanTask(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-complete-mode-task"
	plan := modePlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []modeTaskState{{Title: "Implement solution", Mode: "coding", Status: "running"}},
	}
	if err := persistModePlan(sessionID, plan); err != nil {
		t.Fatalf("persistModePlan error: %v", err)
	}

	if err := CompleteModePlanTask(sessionID); err != nil {
		t.Fatalf("CompleteModePlanTask error: %v", err)
	}

	loaded, err := loadOrCreateModePlan(sessionID, plan.Goal, plan.Mode, "", llm.ModeInstructions{})
	if err != nil {
		t.Fatalf("loadOrCreateModePlan error: %v", err)
	}
	if loaded.Tasks[0].Status != "complete" {
		t.Fatalf("expected task status 'complete', got %q", loaded.Tasks[0].Status)
	}
}

func TestCompleteModePlanTaskNoOp(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// No mode-plan file — should be a no-op.
	if err := CompleteModePlanTask("nonexistent-session"); err != nil {
		t.Fatalf("CompleteModePlanTask should be no-op for missing plan, got error: %v", err)
	}
}

func TestApplyModePropagatesOverlayToContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-overlay-propagation"
	overlay := "Goal Adherence\n- Stay focused on the user's stated goal"
	plan := modePlanState{
		Goal: "fix bug",
		Mode: "coding",
		Tasks: []modeTaskState{{
			Title:          "Implement solution",
			Mode:           "coding",
			Status:         "pending",
			PlannerOverlay: overlay,
		}},
	}
	if err := persistModePlan(sessionID, plan); err != nil {
		t.Fatalf("persistModePlan error: %v", err)
	}

	ctx := modeContext{
		SessionID: sessionID,
		Goal:      "fix bug",
		Config:    legacyConfig{mode: "coding"},
		Options:   Options{}, // PlannerOverlay starts empty
		Display:   ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	_, handled := applyMode(&ctx)
	if handled {
		t.Fatalf("expected handled=false, got handled=true")
	}
	if ctx.Options.PlannerOverlay != overlay {
		t.Fatalf("expected PlannerOverlay to be propagated to ctx.Options, got %q", ctx.Options.PlannerOverlay)
	}
}

func TestModeOverlayPersistedViaRunState(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	overlay := "Goal Adherence\n- Stay focused"

	rs := &runLifecycleState{
		sessionID:      "test-overlay-persist",
		goal:           "fix bug",
		originalPrompt: "fix bug",
		plannerOverlay: overlay,
		sessionStatus:  "success",
	}

	state := rs.baseSessionState()
	if state.PlannerOverlay != overlay {
		t.Fatalf("expected baseSessionState().PlannerOverlay = %q, got %q", overlay, state.PlannerOverlay)
	}
}

func TestAllTasksComplete(t *testing.T) {
	if allTasksComplete(modePlanState{Tasks: []modeTaskState{}}) {
		t.Fatalf("expected false for empty task list")
	}
	if !allTasksComplete(modePlanState{Tasks: []modeTaskState{{Status: "complete"}}}) {
		t.Fatalf("expected true for single complete task")
	}
	if allTasksComplete(modePlanState{Tasks: []modeTaskState{{Status: "complete"}, {Status: "pending"}}}) {
		t.Fatalf("expected false when one task is pending")
	}
	if !allTasksComplete(modePlanState{Tasks: []modeTaskState{{Status: "complete"}, {Status: "complete"}}}) {
		t.Fatalf("expected true when all tasks are complete")
	}
}
