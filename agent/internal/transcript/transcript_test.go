package transcript

import (
	"os"
	"strings"
	"testing"
)

func TestWriteTurn_OmitsPlannerDecisionWhenEmpty(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("decision-empty")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "decision-empty", nil); err != nil {
		t.Fatal(err)
	}

	if err := tr.WriteTurn(1, "Question", "", nil, "Answer", ""); err != nil {
		t.Fatal(err)
	}
	content := tr.Content()
	if strings.Contains(content, "Planner decision:") {
		t.Fatalf("did not expect planner decision line when decision is empty:\n%s", content)
	}
}

func TestFormatGoalSectionVerbatim(t *testing.T) {
	goal := "Investigate root cause\n\nReview logs"
	got := formatGoalSection(goal, "")
	if got != goal {
		t.Fatalf("goal formatting mismatch:\nwant %q\n got %q", goal, got)
	}
}

func TestFormatGoalSectionAppendsTaskDescription(t *testing.T) {
	goal := "Investigate root cause\n\nReview logs"
	got := formatGoalSection(goal, "Trace recent deploys")
	want := "Investigate root cause\n\nReview logs\n\n---\nTrace recent deploys\n---"
	if got != want {
		t.Fatalf("task description formatting mismatch:\nwant %q\n got %q", want, got)
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

	if err := tr.WriteHeader("goal", "", "instr-sess", nil); err != nil {
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

	if err := tr.WriteHeader("goal", "", "feedback-sess", nil); err != nil {
		t.Fatal(err)
	}

	feedback := "This is too marketing sounding. Use a domain-specific name."
	block := "\n=== GOAL UPDATE\n\n" + feedback + "\n"
	if err := tr.AppendRaw(block); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if !strings.Contains(content, "=== GOAL UPDATE") {
		t.Fatalf("missing feedback header in transcript:\n%s", content)
	}
	if !strings.Contains(content, feedback) {
		t.Fatalf("missing feedback body in transcript:\n%s", content)
	}
	if strings.Contains(content, "Planner decision: user-feedback") {
		t.Fatalf("raw feedback should not include a planner decision line:\n%s", content)
	}
}

func TestTranscriptStripsNULBytes(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("nul-sess")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal\x00with\x00nul", "", "nul-sess", nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendRaw("raw\x00block\n"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(1, "Question\x00here", "", nil, "Summary\x00here", "ask\x00"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteFinal("Final\x00answer", 1, false); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if strings.ContainsRune(content, '\x00') {
		t.Fatalf("in-memory transcript contains NUL bytes")
	}

	data, err := os.ReadFile(tr.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(data), '\x00') {
		t.Fatalf("on-disk transcript contains NUL bytes")
	}
}

func TestAppendBlockSanitizesContent(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("block-sess")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "block-sess", nil); err != nil {
		t.Fatal(err)
	}

	block := "raw\x00block\n"
	if err := tr.AppendBlock(block); err != nil {
		t.Fatalf("AppendBlock returned error: %v", err)
	}

	content := tr.Content()
	if strings.ContainsRune(content, '\x00') {
		t.Fatalf("in-memory transcript contains NUL bytes after AppendBlock")
	}
	data, err := os.ReadFile(tr.Path())
	if err != nil {
		t.Fatalf("failed to read transcript: %v", err)
	}
	if strings.ContainsRune(string(data), '\x00') {
		t.Fatalf("on-disk transcript contains NUL bytes after AppendBlock")
	}
}

func TestWriteTurn_CompactsNULBytesBeforeWriting(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("turn-nul")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "turn-nul", nil); err != nil {
		t.Fatal(err)
	}

	withNul := tr.Content() + "\x00\x00\x00"
	if err := os.WriteFile(tr.Path(), []byte(withNul), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.Restore(withNul); err != nil {
		t.Fatal(err)
	}

	if err := tr.WriteTurn(1, "Something", "", nil, "", "ask"); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(tr.Content(), '\x00') {
		t.Fatalf("expected transcript to compact NUL bytes before writing")
	}
}

