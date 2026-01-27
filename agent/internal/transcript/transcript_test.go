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
	if err := tr.WriteHeader("goal", "", "sess", nil); err != nil {
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

func TestDeduplicateFullDiffByFile_RemovesPriorTurns(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("full-diff-sess")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "full-diff-sess", nil); err != nil {
		t.Fatal(err)
	}

	files := []string{"a.txt", "b.txt"}
	if err := tr.WriteTurn(1, "Automatic unified diff post-patch for: a.txt", "", files, "diff1", "full_diff"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(2, "Automatic unified diff post-patch for: a.txt", "", files, "diff2", "full_diff"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(3, "Something else", "", nil, "", "ask"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicateFullDiffByFile("a.txt"); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if strings.Contains(content, "== TURN 1") {
		t.Fatalf("expected turn 1 to be removed:\n%s", content)
	}
	if !strings.Contains(content, "== TURN 2") {
		t.Fatalf("expected latest full diff to remain:\n%s", content)
	}
	if !strings.Contains(content, "== TURN 3") {
		t.Fatalf("expected unrelated turns to remain:\n%s", content)
	}
}

func TestDeduplicateFullDiffByFile_PrefersLastTurnOrder(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("full-diff-step")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "full-diff-step", nil); err != nil {
		t.Fatal(err)
	}

	if err := tr.WriteTurn(5, "Automatic unified diff post-patch for: a.txt", "", []string{"a.txt"}, "diff-old", "full_diff"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(4, "Automatic unified diff post-patch for: a.txt", "", []string{"a.txt"}, "diff-new", "full_diff"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicateFullDiffByFile("a.txt"); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if strings.Contains(content, "diff-old") {
		t.Fatalf("expected earlier full diff to be removed by position:\n%s", content)
	}
	if !strings.Contains(content, "diff-new") {
		t.Fatalf("expected latest in transcript order to remain:\n%s", content)
	}
}

func TestDeduplicateFullDiffByFile_UsesRetrievedPaths(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("full-diff-retrieved")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "full-diff-retrieved", nil); err != nil {
		t.Fatal(err)
	}

	// The question references a different file name, but the retrieved path
	// points to the target file with a leading ./.
	if err := tr.WriteTurn(1, "Automatic unified diff post-patch for: other.md", "", []string{"./README.md"}, "diff-old", "full_diff"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(2, "Automatic unified diff post-patch for: README.md", "", []string{"README.md"}, "diff-new", "full_diff"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicateFullDiffByFile("README.md"); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if strings.Contains(content, "diff-old") {
		t.Fatalf("expected dedupe to key off retrieved paths:\n%s", content)
	}
	if !strings.Contains(content, "diff-new") {
		t.Fatalf("expected latest README.md diff to remain:\n%s", content)
	}
}

func TestDeduplicateFullDiffByFile_RetainsSingleFullDiff(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("full-diff-single")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "full-diff-single", nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(1, "Automatic unified diff post-patch for: a.txt", "", []string{"a.txt"}, "diff-only", "full_diff"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicateFullDiffByFile("a.txt"); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if !strings.Contains(content, "diff-only") {
		t.Fatalf("expected single diff to remain after dedupe:\n%s", content)
	}
}

func TestDeduplicateFullDiffByFile_DoesNotDeleteOtherFileFullDiff(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("full-diff-sess-3")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "full-diff-sess-3", nil); err != nil {
		t.Fatal(err)
	}

	if err := tr.WriteTurn(1, "Automatic unified diff post-patch for: a.txt", "", []string{"a.txt"}, "diff-a-1", "full_diff"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(2, "Automatic unified diff post-patch for: b.txt", "", []string{"b.txt"}, "diff-b-1", "full_diff"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(3, "Automatic unified diff post-patch for: a.txt", "", []string{"a.txt"}, "diff-a-2", "full_diff"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicateFullDiffByFile("a.txt"); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if strings.Contains(content, "diff-a-1") {
		t.Fatalf("expected older a.txt diff to be removed:\n%s", content)
	}
	if !strings.Contains(content, "diff-a-2") {
		t.Fatalf("expected latest a.txt diff to remain:\n%s", content)
	}
	if !strings.Contains(content, "diff-b-1") {
		t.Fatalf("expected b.txt diff to remain:\n%s", content)
	}
}

func TestDeduplicateFullDiffByFile_NoopsForMissing(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("full-diff-sess-2")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "full-diff-sess-2", nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(1, "Something else", "", nil, "", "ask"); err != nil {
		t.Fatal(err)
	}
	before := tr.Content()
	if err := tr.DeduplicateFullDiffByFile("a.txt"); err != nil {
		t.Fatal(err)
	}
	after := tr.Content()
	if before != after {
		t.Fatalf("expected transcript unchanged")
	}
}

func TestDeduplicateFullDiffByFile_StripsNULBytes(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("full-diff-sess-nul")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "full-diff-sess-nul", nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(1, "Automatic unified diff post-patch for: a.txt", "", []string{"a.txt"}, "diff1", "full_diff"); err != nil {
		t.Fatal(err)
	}

	withNul := tr.Content() + "\x00\x00\x00"
	if err := os.WriteFile(tr.Path(), []byte(withNul), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := tr.Restore(withNul); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicateFullDiffByFile("a.txt"); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(tr.Content(), '\x00') {
		t.Fatalf("expected transcript to strip NUL bytes")
	}
}

func TestDeduplicateFullDiffByFile_DoesNotIntroduceNULBytes(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("full-diff-sess-rewrite")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "full-diff-sess-rewrite", nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(1, "Automatic unified diff post-patch for: a.txt", "", []string{"a.txt"}, "diff-a-1", "full_diff"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(2, "Automatic unified diff post-patch for: a.txt", "", []string{"a.txt"}, "diff-a-2", "full_diff"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicateFullDiffByFile("a.txt"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(tr.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(data), '\x00') {
		t.Fatalf("dedupe rewrite should not produce NUL bytes")
	}
}

func TestExtractFullDiffsFromContent_DeduplicatesByFile(t *testing.T) {
	content := strings.Join([]string{
		"== TURN 1",
		"",
		"Automatic unified diff post-patch for: file.txt",
		"",
		"Retrieved File Paths:",
		"* file.txt",
		"",
		"=== ANSWER",
		"",
		"=== UNIFIED DIFF OF PATCHED FILE: file.txt ===",
		"diff one",
		"===",
		"",
		"Planner decision: FULL_DIFF",
		"== TURN 2",
		"",
		"Automatic unified diff post-patch for: file.txt",
		"",
		"=== ANSWER",
		"",
		"=== UNIFIED DIFF OF PATCHED FILE: file.txt ===",
		"diff two",
		"===",
		"",
		"Planner decision: full_diff",
	}, "\n")

	fullDiffs, err := ExtractFullDiffsFromContent(content)
	if err != nil {
		t.Fatalf("ExtractFullDiffsFromContent error: %v", err)
	}

	want := strings.Join([]string{
		"=== UNIFIED DIFF OF PATCHED FILE: file.txt ===",
		"diff two",
		"===",
	}, "\n")
	if fullDiffs != want {
		t.Fatalf("unexpected full diffs:\n%s", fullDiffs)
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

func TestDeduplicatePatchPlan_RemovesPriorSections(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("patch-plan-sess")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "patch-plan-sess", nil); err != nil {
		t.Fatal(err)
	}

	// Add multiple patch plan sections
	if err := tr.AppendRaw("\n== PATCH PLAN CREATED\n\nFirst plan content\n"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendRaw("\n== PATCH PLAN UPDATED\n\nSecond plan content\n"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendRaw("\n== PATCH PLAN CREATED\n\nThird plan content\n"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicatePatchPlan(); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if strings.Contains(content, "First plan content") {
		t.Fatalf("expected first patch plan to be removed:\n%s", content)
	}
	if !strings.Contains(content, "Second plan content") {
		t.Fatalf("expected second patch plan to remain:\n%s", content)
	}
	if !strings.Contains(content, "Third plan content") {
		t.Fatalf("expected latest patch plan to remain:\n%s", content)
	}
}

func TestDeduplicatePatchPlan_PreservesOtherContent(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("patch-plan-preserve")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "patch-plan-preserve", nil); err != nil {
		t.Fatal(err)
	}

	if err := tr.WriteTurn(1, "Question 1", "", nil, "Answer 1", "ask"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendRaw("\n== PATCH PLAN CREATED\n\nOld plan\n"); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(2, "Question 2", "", nil, "Answer 2", "ask"); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendRaw("\n== PATCH PLAN UPDATED\n\nNew plan\n"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicatePatchPlan(); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if !strings.Contains(content, "Answer 1") {
		t.Fatalf("expected turn 1 to remain:\n%s", content)
	}
	if !strings.Contains(content, "Answer 2") {
		t.Fatalf("expected turn 2 to remain:\n%s", content)
	}
	if !strings.Contains(content, "Old plan") {
		t.Fatalf("expected old patch plan to remain:\n%s", content)
	}
	if !strings.Contains(content, "New plan") {
		t.Fatalf("expected new patch plan to remain:\n%s", content)
	}
}

func TestDeduplicatePatchPlan_NoopsWhenNoPlan(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("patch-plan-noop")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "patch-plan-noop", nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.WriteTurn(1, "Question", "", nil, "Answer", "ask"); err != nil {
		t.Fatal(err)
	}

	before := tr.Content()
	if err := tr.DeduplicatePatchPlan(); err != nil {
		t.Fatal(err)
	}
	after := tr.Content()

	if before != after {
		t.Fatalf("expected transcript unchanged when no patch plan exists")
	}
}

func TestDeduplicatePatchPlan_RetainsSinglePlan(t *testing.T) {
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	tmp := t.TempDir()
	_ = os.Chdir(tmp)
	t.Setenv("HOME", tmp)

	tr, err := New("patch-plan-single")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.WriteHeader("goal", "", "patch-plan-single", nil); err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendRaw("\n== PATCH PLAN CREATED\n\nOnly plan\n"); err != nil {
		t.Fatal(err)
	}

	if err := tr.DeduplicatePatchPlan(); err != nil {
		t.Fatal(err)
	}

	content := tr.Content()
	if !strings.Contains(content, "Only plan") {
		t.Fatalf("expected single patch plan to remain:\n%s", content)
	}
}
