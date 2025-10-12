package patcher_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mctpatcher "github.com/tursomari/machtiani/mct/patcher"
	patchersvc "github.com/tursomari/machtiani/patcher"
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
	if len(entries) != 1 {
		t.Fatalf("expected 1 patch file, found %d", len(entries))
	}
	if entries[0].Name() != filepath.Base(res.PatchPath) {
		t.Fatalf("unexpected patch filename: dir entry %q result %q", entries[0].Name(), filepath.Base(res.PatchPath))
	}

	if len(res.FilesModified) != 1 || res.FilesModified[0] != "README.md" {
		t.Fatalf("unexpected files modified: %+v", res.FilesModified)
	}

	patchContent, err := os.ReadFile(res.PatchPath)
	if err != nil {
		t.Fatalf("read patch: %v", err)
	}
	if !strings.Contains(string(patchContent), "+++ b/README.md") {
		t.Fatalf("patch missing expected path header: %s", patchContent)
	}
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
