package discovery

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func withTempDir(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return dir, func() { _ = os.Chdir(wd) }
}

func TestShowCommand(t *testing.T) {
	_, restore := withTempDir(t)
	defer restore()

	writeTempFile(t, ".", "foo.txt", "alpha\nbeta\ngamma\n")
	cmd := showCommand{Paths: []string{"foo.txt"}}
	output, stats, err := cmd.Execute(10, map[string]struct{}{"foo.txt": {}})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if stats.Files != 1 || stats.Lines != 3 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if !strings.Contains(output, "FILE_CONTENT[foo.txt]:") {
		t.Fatalf("missing header: %s", output)
	}
	if !strings.Contains(output, "1: alpha") || !strings.Contains(output, "2: beta") {
		t.Fatalf("missing numbered lines: %s", output)
	}
	if !strings.Contains(output, "END_FILE_CONTENT") {
		t.Fatalf("missing footer: %s", output)
	}
}

func TestOutputTruncation(t *testing.T) {
	_, restore := withTempDir(t)
	defer restore()

	writeTempFile(t, ".", "foo.txt", "1\n2\n3\n4\n5\n")
	cmd := showCommand{Paths: []string{"foo.txt"}}
	output, stats, err := cmd.Execute(3, map[string]struct{}{"foo.txt": {}})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !stats.Truncated {
		t.Fatalf("expected truncation")
	}
	if !strings.Contains(output, "[TRUNCATED: 3 lines limit]") {
		t.Fatalf("expected truncation message: %s", output)
	}
}

func TestFinalizeValidation(t *testing.T) {
	_, restore := withTempDir(t)
	defer restore()

	writeTempFile(t, ".", "foo.txt", "a\nb\nc\n")
	allowed := map[string]struct{}{"foo.txt": {}}
	output := snippetOutput{"foo.txt": {{Start: 1, End: 2}}}
	normalized, err := validateFinalOutput(output, allowed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(normalized["foo.txt"]) != 1 {
		t.Fatalf("unexpected normalized output: %+v", normalized)
	}
	// invalid path
	bad := snippetOutput{"../hack.txt": {{Start: 1, End: 1}}}
	if _, err := validateFinalOutput(bad, allowed); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestLineRangeBounds(t *testing.T) {
	_, restore := withTempDir(t)
	defer restore()

	writeTempFile(t, ".", "foo.txt", "a\nb\nc\n")
	allowed := map[string]struct{}{"foo.txt": {}}
	tooHigh := snippetOutput{"foo.txt": {{Start: 1, End: 5}}}
	if _, err := validateFinalOutput(tooHigh, allowed); err == nil {
		t.Fatalf("expected bounds error")
	}
	backwards := snippetOutput{"foo.txt": {{Start: 3, End: 2}}}
	if _, err := validateFinalOutput(backwards, allowed); err == nil {
		t.Fatalf("expected backwards range error")
	}
}
