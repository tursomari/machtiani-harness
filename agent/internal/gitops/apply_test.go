package gitops

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReversePatchFromFile_Modification(t *testing.T) {
	patch := "diff --git a/foo.txt b/foo.txt\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/foo.txt\n" +
		"+++ b/foo.txt\n" +
		"@@ -1,2 +1,2 @@\n" +
		"-old\n" +
		"+new\n" +
		" context line\n"
	path := writeTempPatch(t, patch)

	reversed, err := ReversePatchFromFile(path)
	if err != nil {
		t.Fatalf("ReversePatchFromFile returned error: %v", err)
	}

	expected := "diff --git a/foo.txt b/foo.txt\n" +
		"index 2222222..1111111 100644\n" +
		"--- b/foo.txt\n" +
		"+++ a/foo.txt\n" +
		"@@ -1,2 +1,2 @@\n" +
		"-new\n" +
		"+old\n" +
		" context line\n"

	if reversed != expected {
		t.Fatalf("unexpected reverse patch:\nexpected:\n%q\nactual:\n%q\n", expected, reversed)
	}
}

func TestReversePatchFromFile_FileCreation(t *testing.T) {
	patch := "diff --git a/new.go b/new.go\n" +
		"new file mode 100644\n" +
		"index 0000000..3333333\n" +
		"--- /dev/null\n" +
		"+++ b/new.go\n" +
		"@@ -0,0 +1,3 @@\n" +
		"+line1\n" +
		"+line2\n" +
		"+line3\n"
	path := writeTempPatch(t, patch)

	reversed, err := ReversePatchFromFile(path)
	if err != nil {
		t.Fatalf("ReversePatchFromFile returned error: %v", err)
	}

	expected := "diff --git a/new.go b/new.go\n" +
		"deleted file mode 100644\n" +
		"index 3333333..0000000\n" +
		"--- b/new.go\n" +
		"+++ /dev/null\n" +
		"@@ -1,3 +0,0 @@\n" +
		"-line1\n" +
		"-line2\n" +
		"-line3\n"

	if reversed != expected {
		t.Fatalf("unexpected reverse patch:\nexpected:\n%q\nactual:\n%q\n", expected, reversed)
	}
}

func TestReversePatchFromFile_Rename(t *testing.T) {
	patch := "diff --git a/old.txt b/new.txt\n" +
		"rename from old.txt\n" +
		"rename to new.txt\n" +
		"--- a/old.txt\n" +
		"+++ b/new.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old content\n" +
		"+new content\n"
	path := writeTempPatch(t, patch)

	reversed, err := ReversePatchFromFile(path)
	if err != nil {
		t.Fatalf("ReversePatchFromFile returned error: %v", err)
	}

	expected := "diff --git a/new.txt b/old.txt\n" +
		"rename from new.txt\n" +
		"rename to old.txt\n" +
		"--- b/new.txt\n" +
		"+++ a/old.txt\n" +
		"@@ -1 +1 @@\n" +
		"-new content\n" +
		"+old content\n"

	if reversed != expected {
		t.Fatalf("unexpected reverse patch:\nexpected:\n%q\nactual:\n%q\n", expected, reversed)
	}
}

func TestReversePatchFromFile_BinaryRename(t *testing.T) {
	patch := "diff --git a/old.bin b/new.bin\n" +
		"similarity index 100%\n" +
		"rename from old.bin\n" +
		"rename to new.bin\n" +
		"Binary files a/old.bin and b/new.bin differ\n"
	path := writeTempPatch(t, patch)

	reversed, err := ReversePatchFromFile(path)
	if err != nil {
		t.Fatalf("ReversePatchFromFile returned error: %v", err)
	}

	expected := "diff --git a/new.bin b/old.bin\n" +
		"similarity index 100%\n" +
		"rename from new.bin\n" +
		"rename to old.bin\n" +
		"Binary files a/new.bin and b/old.bin differ\n"

	if reversed != expected {
		t.Fatalf("unexpected reverse patch:\nexpected:\n%q\nactual:\n%q\n", expected, reversed)
	}
}

func writeTempPatch(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "patch.diff")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write temp patch: %v", err)
	}
	return path
}
