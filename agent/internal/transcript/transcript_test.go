package transcript

import (
	"os"
	"strings"
	"testing"
)

func TestWriteTurn_Patcher(t *testing.T) {
	// Ensure chat dir exists in temp working dir
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)

	t.Setenv("HOME", tmp)

	tr, err := New("sess")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if err := tr.WriteHeader("goal", "sess", nil); err != nil {
		t.Fatal(err)
	}
	// No savedPath line for patcher
	question := "Patcher: [SUCCESS] create minimal fix"
	ans := "✅ STRICT PATCH SUCCESS: create minimal fix\nUpdated files: README.md\nChanges: +1 / -1 (applied to workspace)\n\n" +
		"Patch created: .machtiani/sessions/sess/artifacts/patches/123.patch\n" +
		"files_modified: README.md\n" +
		"insertions: 1\n" +
		"deletions: 1\n"
	if err := tr.WriteTurn(2, question, "", nil, ans, "patch"); err != nil {
		t.Fatal(err)
	}
	content := tr.Content()
	if !strings.Contains(content, "Planner decision: patch") {
		t.Fatalf("missing decision tag in transcript: %s", content)
	}
	if strings.Contains(content, "mct chat:") {
		t.Fatalf("should not include mct chat path for patch turn: %s", content)
	}
}

func TestFormatGoalSectionInlinePrompt(t *testing.T) {
	goal := "Investigate root cause\n\nReview logs"
	got := formatGoalSection(goal)
	want := "***Investigate root cause:Review logs***"
	if got != want {
		t.Fatalf("inline goal formatting mismatch:\nwant %q\n got %q", want, got)
	}
}

func TestFormatGoalSectionProblemContext(t *testing.T) {
	goal := "Task details: Investigate root cause\n\nLine 1 of answer\nLine 2 continued"
	got := formatGoalSection(goal)
	want := "***Investigate root cause***\n\n== PROBLEM:\n\nLine 1 of answer\nLine 2 continued"
	if got != want {
		t.Fatalf("problem goal formatting mismatch:\nwant %q\n got %q", want, got)
	}
}

func TestTranscriptRestoreSeedsContent(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("resume-sess")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	existing := "= mct-agent Transcript\n\nSession: resume-sess\n\n== Goal:\n\nResume testing\n\n"
	if err := tr.Restore(existing); err != nil {
		t.Fatalf("Restore returned error: %v", err)
	}
	if got := tr.Content(); got != existing {
		t.Fatalf("Restore did not seed content:\nwant: %q\n got: %q", existing, got)
	}

	if err := tr.WriteTurn(1, "What next?", "", nil, "Continue", "ask"); err != nil {
		t.Fatalf("WriteTurn after Restore failed: %v", err)
	}

	data, err := os.ReadFile(tr.Path())
	if err != nil {
		t.Fatalf("failed to read transcript file: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, existing) {
		t.Fatalf("file does not start with restored content:\n%s", content)
	}
	if !strings.Contains(content, "Planner decision: ask") {
		t.Fatalf("expected appended turn in content:\n%s", content)
	}
	if strings.Contains(content, "Question:\nWhat next?") {
		t.Fatalf("question label should not be prefixed in transcript:\n%s", content)
	}
}

func TestWriteTurnInstructionSkipsQuestionLabel(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("instr-sess")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "instr-sess", nil); err != nil {
		t.Fatal(err)
	}

	question := "Instruction: Inspect the build logs for errors"
	if err := tr.WriteTurn(1, question, "", nil, "", "ask"); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if strings.Contains(content, "Question:\nInstruction:") {
		t.Fatalf("question label should be omitted for instruction prompts:\n%s", content)
	}
	if !strings.Contains(content, question) {
		t.Fatalf("instruction text missing from transcript:\n%s", content)
	}
}

func TestAppendRaw_UserFeedbackSection(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("feedback-sess")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "feedback-sess", nil); err != nil {
		t.Fatal(err)
	}

	feedback := "This is too marketing sounding. Use a domain-specific name."
	block := "\n=== USER FEEDBACK\n\n" + feedback + "\n"
	if err := tr.AppendRaw(block); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if !strings.Contains(content, "=== USER FEEDBACK") {
		t.Fatalf("missing feedback header in transcript:\n%s", content)
	}
	if !strings.Contains(content, feedback) {
		t.Fatalf("missing feedback body in transcript:\n%s", content)
	}
	if strings.Contains(content, "Planner decision: user-feedback") {
		t.Fatalf("raw feedback should not include a planner decision line:\n%s", content)
	}
}
