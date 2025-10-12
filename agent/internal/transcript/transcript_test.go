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

	tr, err := New()
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if err := tr.WriteHeader("goal", "sess", nil); err != nil {
		t.Fatal(err)
	}
	// No savedPath line for patcher
	question := "Patcher: create minimal fix"
	ans := "Patch created: .machtiani/artifacts/patches/sess/123.patch\nfiles_modified: [README.md]\ninsertions: 1\ndeletions: 1\n"
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
