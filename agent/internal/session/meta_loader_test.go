package session

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestInstructionsToTasksFromTomlDocument(t *testing.T) {
	doc := llm.MetaInstructions{
		Format: llm.MetaInstructionsFormatTOML,
		Path:   "coding.toml",
		Tasks: []llm.MetaInstructionTask{
			{Step: 2, Title: "Second", Description: "Second desc", ShellAgent: boolPtr(true), PatchMode: boolPtr(true)},
			{Step: 1, Title: "First", Description: "First desc", ShellAgent: boolPtr(false), PatchMode: boolPtr(false)},
		},
	}

	tasks := instructionsToTasks("overall goal", "coding", doc)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Title != "First" {
		t.Fatalf("expected first task title to be First, got %q", tasks[0].Title)
	}
	if tasks[1].ShellAgent == nil || !*tasks[1].ShellAgent {
		t.Fatalf("expected second task to enable shell agent")
	}
	if tasks[1].PatchMode == nil || !*tasks[1].PatchMode {
		t.Fatalf("expected second task to enable patch mode")
	}
	if !containsSubstr(t, tasks[0].Goal, "First desc") {
		t.Fatalf("expected goal to include description, got %q", tasks[0].Goal)
	}
}

func TestInstructionsToTasksFromTextDocument(t *testing.T) {
	doc := llm.MetaInstructions{
		Format: llm.MetaInstructionsFormatText,
		Raw:    "- plan\n- execute\n",
	}

	tasks := instructionsToTasks("goal", "coding", doc)
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Title != "plan" {
		t.Fatalf("unexpected first task title: %q", tasks[0].Title)
	}
	if tasks[1].ShellAgent != nil {
		t.Fatalf("text tasks should not set shell agent override by default")
	}
}

func TestInstructionsToTasksEmptyTomlFallsBackToDefault(t *testing.T) {
	doc := llm.MetaInstructions{Format: llm.MetaInstructionsFormatTOML}
	tasks := instructionsToTasks("goal", "coding", doc)
	if len(tasks) == 0 {
		t.Fatalf("expected fallback tasks")
	}
	if tasks[0].Title == "" {
		t.Fatalf("expected first fallback task to have title")
	}
}

func TestApplyTaskOverrides(t *testing.T) {
	opts := Options{}
	task := metaTaskState{ShellAgent: boolPtr(true), PatchMode: boolPtr(true)}

	applyTaskOverrides(&opts, task)

	if !opts.Config.ShellAgent {
		t.Fatalf("expected shell agent to be enabled")
	}
	if !opts.Config.Patch {
		t.Fatalf("expected patch mode to be enabled")
	}

	second := metaTaskState{ShellAgent: boolPtr(false), PatchMode: boolPtr(false)}
	applyTaskOverrides(&opts, second)
	if opts.Config.ShellAgent {
		t.Fatalf("expected shell agent to be disabled")
	}
	if opts.Config.Patch {
		t.Fatalf("expected patch mode to be disabled")
	}

	opts.Config.ShellAgent = true
	opts.Config.Patch = true
	inherit := metaTaskState{}
	applyTaskOverrides(&opts, inherit)
	if !opts.Config.ShellAgent {
		t.Fatalf("expected shell agent to remain enabled when override missing")
	}
	if !opts.Config.Patch {
		t.Fatalf("expected patch mode to remain enabled when override missing")
	}
}

func TestUpdateMetaPlanProgress(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	sessionID := "test-progress"
	plan := metaPlanState{
		Goal:  "goal",
		Mode:  "coding",
		Tasks: []metaTaskState{{Title: "Task", Goal: "Task goal", Mode: "coding", Status: "pending"}},
	}
	if err := persistMetaPlan(sessionID, plan); err != nil {
		t.Fatalf("persistMetaPlan error: %v", err)
	}
	progress := &PlannerProgressState{SuccessFiles: []string{"LICENSE", "docs/README.md"}, AppliedPatches: 2}
	if err := UpdateMetaPlanProgress(sessionID, progress); err != nil {
		t.Fatalf("UpdateMetaPlanProgress error: %v", err)
	}
	loaded, err := loadOrCreateMetaPlan(sessionID, plan.Goal, plan.Mode, "", llm.MetaInstructions{})
	if err != nil {
		t.Fatalf("loadOrCreateMetaPlan error: %v", err)
	}
	if loaded.PlannerProgress == nil {
		t.Fatalf("expected planner progress to be persisted")
	}
	if loaded.PlannerProgress.AppliedPatches != progress.AppliedPatches {
		t.Fatalf("expected applied patches %d, got %d", progress.AppliedPatches, loaded.PlannerProgress.AppliedPatches)
	}
	if len(loaded.PlannerProgress.SuccessFiles) != len(progress.SuccessFiles) {
		t.Fatalf("expected %d success files, got %d", len(progress.SuccessFiles), len(loaded.PlannerProgress.SuccessFiles))
	}
	for i, want := range progress.SuccessFiles {
		if loaded.PlannerProgress.SuccessFiles[i] != want {
			t.Fatalf("success file[%d] = %q, want %q", i, loaded.PlannerProgress.SuccessFiles[i], want)
		}
	}
}

func containsSubstr(t *testing.T, s, sub string) bool {
	t.Helper()
	return strings.Contains(s, sub)
}

func boolPtr(v bool) *bool {
	return &v
}
