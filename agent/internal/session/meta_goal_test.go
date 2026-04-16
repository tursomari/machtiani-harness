package session

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrateTaskUserGuidance(t *testing.T) {
	tests := []struct {
		name     string
		existing string
		guidance string
		expected string
	}{
		{
			name:     "appends guidance to existing guidance",
			existing: "Update licensing across repository",
			guidance: "Focus on root LICENSE only",
			expected: "Update licensing across repository\n\nFocus on root LICENSE only",
		},
		{
			name:     "empty existing picks guidance",
			existing: "",
			guidance: "Limit scope to package.json",
			expected: "Limit scope to package.json",
		},
		{
			name:     "guidance already present avoids duplication",
			existing: "Update licensing across repository\n\nFocus on root LICENSE only",
			guidance: "Focus on root LICENSE only",
			expected: "Update licensing across repository\n\nFocus on root LICENSE only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := integrateTaskUserGuidance(tt.existing, tt.guidance)
			if got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestComposeRevisedGoalPromptIncludesInstruction(t *testing.T) {
	base := "***Diagnose***\n\nUpdate licensing across repository"
	guidance := "Only modify ./LICENSE"
	prompt := composeRevisedGoalPrompt(base, guidance)
	if !strings.Contains(prompt, base) {
		t.Fatalf("expected prompt to include base content, got %q", prompt)
	}
	if !strings.Contains(prompt, guidance) {
		t.Fatalf("expected prompt to include guidance, got %q", prompt)
	}
	if !strings.Contains(prompt, "Revised Goal:") {
		t.Fatalf("expected prompt to instruct revised goal output, got %q", prompt)
	}
}

func TestExtractUserFeedbackStripsRevisedGoalWrapper(t *testing.T) {
	base := "***Diagnose***\n\nUpdate licensing across repository"
	guidance := "Only modify ./LICENSE\n\nLeave package metadata untouched."
	prompt := composeRevisedGoalPrompt(base, guidance)

	if got := extractUserFeedback(prompt); got != guidance {
		t.Fatalf("expected extracted guidance %q, got %q", guidance, got)
	}
}

func TestExtractUserFeedbackLeavesPlainPromptUntouched(t *testing.T) {
	prompt := "List any untracked files or modified tracked files in project root and in git submodule"

	if got := extractUserFeedback(prompt); got != prompt {
		t.Fatalf("expected plain prompt %q, got %q", prompt, got)
	}
}

func TestExtractUserFeedbackStripsExactSessionWrapper(t *testing.T) {
	raw := `list any untracked files or modified tracked files in project root and in git submoodule
"""

Reevaluate the task in light of this guidance. Begin by writing a single line starting with "Revised Goal:" that captures the updated objective, then continue execution anchored on that revised goal.`
	want := "list any untracked files or modified tracked files in project root and in git submoodule"

	if got := extractUserFeedback(raw); got != want {
		t.Fatalf("expected extracted guidance %q, got %q", want, got)
	}
}

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

func TestUpdateChildSessionGoalPersists(t *testing.T) {
	tmpDir := t.TempDir()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() {
		_ = os.Chdir(prevWD)
	}()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}

	cmd := exec.Command("git", "init")
	cmd.Stdout = &bytes.Buffer{}
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}

	sessionID := "test-session"
	original := SessionState{SessionID: sessionID, Goal: "Original goal", OriginalPrompt: "Original goal"}
	if err := SaveSessionState(original); err != nil {
		t.Fatalf("save session state: %v", err)
	}

	revised := composeTaskPrompt("", metaTaskState{Instruction: original.Goal, UserGuidance: integrateTaskUserGuidance("", "Focus on root LICENSE")}, "", true)
	if err := updateChildSessionGoal(sessionID, revised); err != nil {
		t.Fatalf("update child session goal: %v", err)
	}

	statePath := filepath.Join(tmpDir, ".machtiani", "sessions", sessionID, "session-state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read session state: %v", err)
	}
	var current SessionState
	if err := json.Unmarshal(data, &current); err != nil {
		t.Fatalf("unmarshal session state: %v", err)
	}
	if current.Goal != revised {
		t.Fatalf("expected goal %q, got %q", revised, current.Goal)
	}
	if current.OriginalPrompt != original.OriginalPrompt {
		t.Fatalf("expected original prompt %q, got %q", original.OriginalPrompt, current.OriginalPrompt)
	}
}
