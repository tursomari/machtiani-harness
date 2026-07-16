package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func writeFile(t *testing.T, root, name, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openFixture(t *testing.T, root string) *Workspace {
	t.Helper()
	w, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestListFilesIgnorePrecedenceAndHardExcludes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.log\nignored.txt\nsub/*.tmp\n")
	writeFile(t, root, ".ignore", "!ignored.txt\nfrom-ignore.txt\n")
	writeFile(t, root, ".rgignore", "!from-ignore.txt\n*.secret\n")
	writeFile(t, root, "sub/.gitignore", "nested.txt\n!keep.tmp\n")
	for _, name := range []string{
		"visible.go", ".env", "ignored.txt", "from-ignore.txt", "debug.log", "token.secret",
		"sub/nested.txt", "sub/drop.tmp", "sub/keep.tmp", "node_modules/pkg.js", "image.png",
	} {
		writeFile(t, root, name, name+"\n")
	}
	w := openFixture(t, root)
	got, stats, err := w.ListFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".env", ".gitignore", ".ignore", ".rgignore", "from-ignore.txt", "ignored.txt", "sub/.gitignore", "sub/keep.tmp", "visible.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("ListFiles() = %#v, want %#v", got, want)
	}
	if stats.Skipped[SkipIgnored] == 0 || stats.Skipped[SkipExcluded] == 0 {
		t.Fatalf("missing ignore/exclude stats: %#v", stats.Skipped)
	}
}

func TestListFilesFollowsInRootSymlinksAndKeepsLogicalPaths(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "actual/file.go", "package actual\n")
	writeFile(t, root, "vendor/hidden.go", "package hidden\n")
	if err := os.Symlink("actual/file.go", filepath.Join(root, "alias.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("actual", filepath.Join(root, "aliasdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "actual", "file.go"), filepath.Join(root, "absolute.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("vendor/hidden.go", filepath.Join(root, "vendor-alias.go")); err != nil {
		t.Fatal(err)
	}
	w := openFixture(t, root)
	got, _, err := w.ListFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"absolute.go", "actual/file.go", "alias.go", "aliasdir/file.go"} {
		if !slices.Contains(got, want) {
			t.Errorf("missing logical symlink path %q in %#v", want, got)
		}
	}
	if slices.Contains(got, "vendor-alias.go") {
		t.Fatalf("physical hard exclusion bypassed: %#v", got)
	}
}

func TestListFilesSkipsOutsideBrokenAndCyclicSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{
		"outside.go": outside,
		"broken.go":  "missing.go",
		"cycle-a":    "cycle-b",
		"cycle-b":    "cycle-a",
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	w := openFixture(t, root)
	got, stats, err := w.ListFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unexpected paths: %#v", got)
	}
	if stats.Skipped[SkipOutside] != 1 || stats.Skipped[SkipBroken] != 1 || stats.Skipped[SkipCycle] != 2 {
		t.Fatalf("unexpected skip stats: %#v", stats.Skipped)
	}
}

func TestReadLinesRangeRegexpAndSymlink(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "source.txt", "one\ntwo/path\nthree\nTODO four")
	if err := os.Symlink("source.txt", filepath.Join(root, "alias.txt")); err != nil {
		t.Fatal(err)
	}
	w := openFixture(t, root)
	ranged, _, err := w.ReadLines(context.Background(), "alias.txt", Selector{StartLine: 2, EndLine: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ranged, []string{"two/path", "three"}) {
		t.Fatalf("range = %#v", ranged)
	}
	matched, _, err := w.ReadLines(context.Background(), "source.txt", Selector{Pattern: regexp.MustCompile(`TODO|two/path`)})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(matched, []string{"two/path", "TODO four"}) {
		t.Fatalf("matches = %#v", matched)
	}
}

func TestReadLinesRejectsOutsideSymlink(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside.txt")); err != nil {
		t.Fatal(err)
	}
	w := openFixture(t, root)
	_, _, err := w.ReadLines(context.Background(), "outside.txt", Selector{StartLine: 1, EndLine: 1})
	var pathErr *PathError
	if !errors.As(err, &pathErr) || pathErr.Reason != SkipOutside {
		t.Fatalf("error = %v, want outside-workspace PathError", err)
	}
}

func TestListDirStableFormatAndSymlinkKinds(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "dir/z.txt", "z\n")
	writeFile(t, root, "dir/a.txt", "a\n")
	if err := os.Symlink("a.txt", filepath.Join(root, "dir", "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("dir", filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	w := openFixture(t, root)
	entries, _, err := w.ListDir(context.Background(), "dir")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{entries[0].Name, entries[1].Name, entries[2].Name}; !slices.Equal(got, []string{"a.txt", "link.txt", "z.txt"}) {
		t.Fatalf("order = %#v", got)
	}
	if entries[1].Kind != "symlink:file" || entries[1].LinkTarget != "a.txt" {
		t.Fatalf("symlink entry = %#v", entries[1])
	}
	line := entries[1].Line()
	if !strings.Contains(line, "symlink:file\t") || !strings.Contains(line, "\"link.txt\"\t->\t\"a.txt\"") {
		t.Fatalf("line = %q", line)
	}
	linkedEntries, _, err := w.ListDir(context.Background(), "dir-link")
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{linkedEntries[0].Name, linkedEntries[1].Name, linkedEntries[2].Name}; !slices.Equal(got, []string{"a.txt", "link.txt", "z.txt"}) {
		t.Fatalf("directory symlink listing order = %#v", got)
	}
}

func TestListFilesHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "file.go", "package fixture\n")
	w := openFixture(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := w.ListFiles(ctx)
	if err == nil || err != context.Canceled {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
