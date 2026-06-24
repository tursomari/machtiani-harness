package session

import (
	"os"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestInstructionsToTasksFromTomlDocument(t *testing.T) {
	doc := llm.ModeInstructions{
		Format: llm.ModeInstructionsFormatTOML,
		Path:   "coding.toml",
		Task: llm.ModeTask{
			Title:        "First",
			Description:  "First desc",
			Instruction:  "Review the first task",
			SystemPrompt: "Extra planner note",
		},
	}

	tasks := instructionsToTasks("overall goal", "coding", doc)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Title != "First" {
		t.Fatalf("expected task title to be First, got %q", tasks[0].Title)
	}
	if tasks[0].Description != "First desc" {
		t.Fatalf("expected description metadata to be preserved, got %q", tasks[0].Description)
	}
	if tasks[0].Instruction != "Review the first task" {
		t.Fatalf("expected instruction to use explicit instruction field, got %q", tasks[0].Instruction)
	}
	if tasks[0].PlannerOverlay != "Extra planner note" {
		t.Fatalf("unexpected planner overlay: %q", tasks[0].PlannerOverlay)
	}
}

func TestInstructionsToTasksTomlFallsBackToTitleWhenInstructionMissing(t *testing.T) {
	doc := llm.ModeInstructions{
		Format: llm.ModeInstructionsFormatTOML,
		Task:   llm.ModeTask{Title: "Validate", Description: "Metadata only"},
	}

	tasks := instructionsToTasks("goal", "coding", doc)
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Instruction != "Validate" {
		t.Fatalf("expected title fallback for instruction, got %q", tasks[0].Instruction)
	}
	if tasks[0].PlannerOverlay != "" {
		t.Fatalf("expected no planner overlay without system_prompt, got %q", tasks[0].PlannerOverlay)
	}
}

func TestInstructionsToTasksFromTextDocument(t *testing.T) {
	doc := llm.ModeInstructions{
		Format: llm.ModeInstructionsFormatText,
		Raw:    "- plan\n- execute\n",
	}

	tasks := instructionsToTasks("goal", "coding", doc)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Title != "plan" {
		t.Fatalf("unexpected first task title: %q", tasks[0].Title)
	}
}

func TestInstructionsToTasksEmptyTomlFallsBackToDefault(t *testing.T) {
	doc := llm.ModeInstructions{Format: llm.ModeInstructionsFormatTOML}
	tasks := instructionsToTasks("goal", "coding", doc)
	if len(tasks) == 0 {
		t.Fatalf("expected fallback tasks")
	}
	if tasks[0].Title == "" {
		t.Fatalf("expected first fallback task to have title")
	}
}

func TestUpdateModePlanProgressNoLongerPersistsProgressInModePlan(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	sessionID := "test-progress"
	plan := modePlanState{
		Goal:  "goal",
		Mode:  "coding",
		Tasks: []modeTaskState{{Title: "Task", Instruction: "Task goal", Mode: "coding", Status: "pending"}},
	}
	if err := persistModePlan(sessionID, plan); err != nil {
		t.Fatalf("persistModePlan error: %v", err)
	}
	progress := &conversation.PlannerProgressState{
		SuccessFiles: []string{"LICENSE", "docs/README.md"},
	}
	if err := UpdateModePlanProgress(sessionID, progress); err != nil {
		t.Fatalf("UpdateModePlanProgress error: %v", err)
	}

	planPath, err := modePlanPath(sessionID)
	if err != nil {
		t.Fatalf("modePlanPath: %v", err)
	}
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatalf("read mode plan: %v", err)
	}
	if strings.Contains(string(raw), "planner_progress") {
		t.Fatalf("mode-plan.json must not contain planner_progress; got %s", string(raw))
	}
}
