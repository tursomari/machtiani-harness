package patcher_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
)

func TestServiceWritesPatchesToArtifactsDirectory(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")

	baseFile := filepath.Join(repo, "README.md")
	if err := os.WriteFile(baseFile, []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}

	sess := "sess-123"

	svc := patchersvc.NewService(patchersvc.WithClock(func() time.Time {
		return time.Date(2024, 10, 12, 1, 2, 3, 0, time.UTC)
	}))

	instr := mctpatcher.Instructions{
		Edits: []mctpatcher.Edit{{
			Path:       "README.md",
			Mode:       mctpatcher.ModeRewrite,
			NewContent: "after\n",
		}},
	}

	res, err := svc.ApplyAndGeneratePatch(context.Background(), mctpatcher.PatchParams{
		RepoRoot:     repo,
		SessionID:    sess,
		Instructions: instr,
	})
	if err != nil {
		t.Fatalf("ApplyAndGeneratePatch: %v", err)
	}

	expectedDir := filepath.Join(repo, ".machtiani", "sessions", sess, "artifacts", "patches")
	if got := filepath.Dir(res.PatchPath); got != expectedDir {
		t.Fatalf("unexpected patch directory: got %q want %q", got, expectedDir)
	}

	if _, err := os.Stat(res.PatchPath); err != nil {
		t.Fatalf("patch file missing: %v", err)
	}

	entries, err := os.ReadDir(expectedDir)
	if err != nil {
		t.Fatalf("read artifacts dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected patch file and after-state directory, found %d entries", len(entries))
	}
	patchName := filepath.Base(res.PatchPath)
	afterDirName := strings.TrimSuffix(patchName, filepath.Ext(patchName)) + "-after"
	var foundPatch, foundAfter bool
	for _, entry := range entries {
		switch entry.Name() {
		case patchName:
			foundPatch = true
		case afterDirName:
			if !entry.IsDir() {
				t.Fatalf("expected after-state entry %q to be a directory", afterDirName)
			}
			foundAfter = true
		}
	}
	if !foundPatch || !foundAfter {
		t.Fatalf("expected artifacts to contain patch %q and after-state dir %q", patchName, afterDirName)
	}

	assertPatchContent(t, res)
	assertAfterStateArtifacts(t, res)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func assertPatchContent(t *testing.T, res *mctpatcher.PatchResult) {
	t.Helper()
	if len(res.FilesModified) != 1 || res.FilesModified[0] != "README.md" {
		t.Fatalf("unexpected files modified: %+v", res.FilesModified)
	}
	patchContent, err := os.ReadFile(res.PatchPath)
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	if len(patchContent) == 0 {
		t.Fatal("patch content was empty")
	}
}

func assertAfterStateArtifacts(t *testing.T, res *mctpatcher.PatchResult) {
	t.Helper()
	if res.AfterStateDir == "" {
		t.Fatal("expected AfterStateDir to be populated")
	}
	if res.ManifestPath == "" {
		t.Fatal("expected ManifestPath to be populated")
	}
	filesDir := filepath.Join(res.AfterStateDir, "files")
	if st, err := os.Stat(filesDir); err != nil {
		t.Fatalf("after-state files directory missing: %v", err)
	} else if !st.IsDir() {
		t.Fatalf("after-state files path is not a directory: %s", filesDir)
	}
	contents, err := os.ReadFile(filepath.Join(filesDir, "README.md"))
	if err != nil {
		t.Fatalf("read persisted after-state file: %v", err)
	}
	if string(contents) != "after\n" {
		t.Fatalf("unexpected after-state content: %q", contents)
	}
	manifestBytes, err := os.ReadFile(res.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest struct {
		Files []struct {
			Path    string `json:"path"`
			Deleted bool   `json:"deleted"`
		}
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "README.md" {
		t.Fatalf("unexpected manifest entries: %+v", manifest.Files)
	}
	if manifest.Files[0].Deleted {
		t.Fatalf("manifest incorrectly marked README.md as deleted")
	}
}

func TestServiceCreatesPlaceholderForPatchOnMissingFile(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init")

	sess := "sess-patch-new"

	svc := patchersvc.NewService()

	instr := mctpatcher.Instructions{
		Edits: []mctpatcher.Edit{{
			Path:      "lib/newfile.js",
			Mode:      mctpatcher.ModePatch,
			StartLine: 1,
			EndLine:   0,
			NewContent: strings.Join([]string{
				"'use strict'",
				"",
				"module.exports = function () {",
				"	return 'hello'",
				"}",
				"",
			}, "\n") + "\n",
		}},
	}

	res, err := svc.ApplyAndGeneratePatch(context.Background(), mctpatcher.PatchParams{
		RepoRoot:      repo,
		WorkspaceRoot: repo,
		SessionID:     sess,
		Instructions:  instr,
	})
	if err != nil {
		t.Fatalf("ApplyAndGeneratePatch: %v", err)
	}

	if len(res.FilesModified) != 1 || res.FilesModified[0] != "lib/newfile.js" {
		t.Fatalf("unexpected files modified: %+v", res.FilesModified)
	}

	contents, err := os.ReadFile(filepath.Join(repo, "lib", "newfile.js"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	got := strings.TrimRight(string(contents), "\n")
	want := strings.TrimRight(instr.Edits[0].NewContent, "\n")
	if got != want {
		t.Fatalf("unexpected file content\n-- got --\n%s\n-- want --\n%s", contents, instr.Edits[0].NewContent)
	}
}
