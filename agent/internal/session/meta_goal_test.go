package session

import (
	"bytes"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func TestMetaOrchestrateCompletedPlanWithResumePromptReturnsHandledFalse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-completed-resume"
	plan := metaPlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []metaTaskState{{Title: "Implement solution", Mode: "coding", Status: "complete"}},
	}
	if err := persistMetaPlan(sessionID, plan); err != nil {
		t.Fatalf("persistMetaPlan error: %v", err)
	}

	ctx := metaContext{
		SessionID:    sessionID,
		Goal:         "fix bug",
		ResumePrompt: "add more tests",
		Config:       legacyConfig{mode: "coding"},
		Display:      ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	result, handled := metaOrchestrate(&ctx)
	if handled {
		t.Fatalf("expected handled=false for completed plan with resume prompt, got handled=%v", handled)
	}
	if len(result.Plan.Tasks) == 0 {
		t.Fatalf("expected non-empty plan tasks")
	}
}

func TestMetaOrchestrateCompletedPlanWithoutResumePromptReturnsHandledFalse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-completed-no-resume"
	plan := metaPlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []metaTaskState{{Title: "Implement solution", Mode: "coding", Status: "complete"}},
	}
	if err := persistMetaPlan(sessionID, plan); err != nil {
		t.Fatalf("persistMetaPlan error: %v", err)
	}

	ctx := metaContext{
		SessionID:    sessionID,
		Goal:         "fix bug",
		ResumePrompt: "",
		Config:       legacyConfig{mode: "coding"},
		Display:      ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	// With the single-session model, completed plan without resume prompt
	// returns handled=false (the planner loop handles finalization).
	_, handled := metaOrchestrate(&ctx)
	if handled {
		t.Fatalf("expected handled=false for completed plan without resume prompt, got handled=%v", handled)
	}
}

func TestMetaOrchestratePendingTaskReturnsHandledFalse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-pending-task"
	plan := metaPlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []metaTaskState{{Title: "Implement solution", Mode: "coding", Status: "pending"}},
	}
	if err := persistMetaPlan(sessionID, plan); err != nil {
		t.Fatalf("persistMetaPlan error: %v", err)
	}

	ctx := metaContext{
		SessionID: sessionID,
		Goal:      "fix bug",
		Config:    legacyConfig{mode: "coding"},
		Options:   Options{},
		Display:   ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	result, handled := metaOrchestrate(&ctx)
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

func TestMetaOrchestrateSuspendedTaskReturnsHandledFalse(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-suspended-task"
	plan := metaPlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []metaTaskState{{Title: "Implement solution", Mode: "coding", Status: "suspended"}},
	}
	if err := persistMetaPlan(sessionID, plan); err != nil {
		t.Fatalf("persistMetaPlan error: %v", err)
	}

	ctx := metaContext{
		SessionID:    sessionID,
		Goal:         "fix bug",
		ResumePrompt: "the answer is 42",
		Config:       legacyConfig{mode: "coding"},
		Options:      Options{},
		Display:      ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	result, handled := metaOrchestrate(&ctx)
	if handled {
		t.Fatalf("expected handled=false for suspended task with resume prompt, got handled=%v", handled)
	}
	if result.Plan.Tasks[0].Status != "running" {
		t.Fatalf("expected task status to be 'running' after resume, got %q", result.Plan.Tasks[0].Status)
	}
}

func TestMetaOrchestrateSuspendedTaskWithoutResumePromptResetsToRunning(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-suspended-no-prompt"
	plan := metaPlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []metaTaskState{{Title: "Implement solution", Mode: "coding", Status: "suspended"}},
	}
	if err := persistMetaPlan(sessionID, plan); err != nil {
		t.Fatalf("persistMetaPlan error: %v", err)
	}

	ctx := metaContext{
		SessionID: sessionID,
		Goal:      "fix bug",
		Config:    legacyConfig{mode: "coding"},
		Options:   Options{},
		Display:   ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	result, handled := metaOrchestrate(&ctx)
	if handled {
		t.Fatalf("expected handled=false for suspended task without resume prompt, got handled=%v", handled)
	}
	if result.Plan.Tasks[0].Status != "running" {
		t.Fatalf("expected task status to be 'running' even without resume prompt, got %q", result.Plan.Tasks[0].Status)
	}
}

func TestCompleteMetaPlanTask(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-complete-meta-task"
	plan := metaPlanState{
		Goal:  "fix bug",
		Mode:  "coding",
		Tasks: []metaTaskState{{Title: "Implement solution", Mode: "coding", Status: "running"}},
	}
	if err := persistMetaPlan(sessionID, plan); err != nil {
		t.Fatalf("persistMetaPlan error: %v", err)
	}

	if err := CompleteMetaPlanTask(sessionID); err != nil {
		t.Fatalf("CompleteMetaPlanTask error: %v", err)
	}

	loaded, err := loadOrCreateMetaPlan(sessionID, plan.Goal, plan.Mode, "", llm.MetaInstructions{})
	if err != nil {
		t.Fatalf("loadOrCreateMetaPlan error: %v", err)
	}
	if loaded.Tasks[0].Status != "complete" {
		t.Fatalf("expected task status 'complete', got %q", loaded.Tasks[0].Status)
	}
}

func TestCompleteMetaPlanTaskNoOp(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// No meta-plan file — should be a no-op.
	if err := CompleteMetaPlanTask("nonexistent-session"); err != nil {
		t.Fatalf("CompleteMetaPlanTask should be no-op for missing plan, got error: %v", err)
	}
}

func TestMetaOrchestratePropagatesOverlayToContext(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	sessionID := "test-overlay-propagation"
	overlay := "Goal Adherence\n- Stay focused on the user's stated goal"
	plan := metaPlanState{
		Goal: "fix bug",
		Mode: "coding",
		Tasks: []metaTaskState{{
			Title:          "Implement solution",
			Mode:           "coding",
			Status:         "pending",
			PlannerOverlay: overlay,
		}},
	}
	if err := persistMetaPlan(sessionID, plan); err != nil {
		t.Fatalf("persistMetaPlan error: %v", err)
	}

	ctx := metaContext{
		SessionID: sessionID,
		Goal:      "fix bug",
		Config:    legacyConfig{mode: "coding"},
		Options:   Options{}, // PlannerOverlay starts empty
		Display:   ui.NewTerminalDisplay(&bytes.Buffer{}, nil, ""),
	}

	_, handled := metaOrchestrate(&ctx)
	if handled {
		t.Fatalf("expected handled=false, got handled=true")
	}
	if ctx.Options.PlannerOverlay != overlay {
		t.Fatalf("expected PlannerOverlay to be propagated to ctx.Options, got %q", ctx.Options.PlannerOverlay)
	}
}

func TestMetaOverlayPersistedViaRunState(t *testing.T) {
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
	if allTasksComplete(metaPlanState{Tasks: []metaTaskState{}}) {
		t.Fatalf("expected false for empty task list")
	}
	if !allTasksComplete(metaPlanState{Tasks: []metaTaskState{{Status: "complete"}}}) {
		t.Fatalf("expected true for single complete task")
	}
	if allTasksComplete(metaPlanState{Tasks: []metaTaskState{{Status: "complete"}, {Status: "pending"}}}) {
		t.Fatalf("expected false when one task is pending")
	}
	if !allTasksComplete(metaPlanState{Tasks: []metaTaskState{{Status: "complete"}, {Status: "complete"}}}) {
		t.Fatalf("expected true when all tasks are complete")
	}
}
