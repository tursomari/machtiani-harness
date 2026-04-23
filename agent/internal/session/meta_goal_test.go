package session

import (
	"bytes"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/ui"
)

func TestComposeTaskPromptUsesTitleWhenDescriptionExists(t *testing.T) {
	prompt := composeTaskPrompt("Fix the issue", metaTaskState{
		Title:       "Create an issue for the engineering team",
		Description: "Create an issue for the engineering team that solves the Goal. Do not make any code changes.",
		Instruction: "Create an issue for the engineering team",
	}, "", true)

	if !strings.Contains(prompt, "***Create an issue for the engineering team***") {
		t.Fatalf("expected prompt to include task title, got %q", prompt)
	}
	if strings.Contains(prompt, "Do not make any code changes") {
		t.Fatalf("expected prompt to keep description out of composed goal, got %q", prompt)
	}
	if !strings.Contains(prompt, "Original prompt:\nFix the issue") {
		t.Fatalf("expected prompt to include original prompt, got %q", prompt)
	}
}

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

	outcome, handled := metaOrchestrate(ctx)
	if handled {
		t.Fatalf("expected handled=false for completed plan with resume prompt, got handled=%v", handled)
	}
	if outcome.Plan.Goal != "" || len(outcome.Plan.Tasks) > 0 {
		t.Fatalf("expected empty outcome plan, got %+v", outcome.Plan)
	}
}

func TestMetaOrchestrateCompletedPlanWithoutResumePromptReturnsHandledTrue(t *testing.T) {
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

	outcome, handled := metaOrchestrate(ctx)
	if !handled {
		t.Fatalf("expected handled=true for completed plan without resume prompt, got handled=%v", handled)
	}
	if outcome.FinalAnswer == "" {
		t.Fatalf("expected non-empty summary for completed plan")
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
