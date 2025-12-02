package patcher_test

import (
    "context"
    "os"
    "path/filepath"
    "testing"

    mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
    patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
)

func TestServiceConvertsRangePatchToRewriteWhenFullMode(t *testing.T) {
    repo := t.TempDir()
    runGit(t, repo, "init")
    runGit(t, repo, "config", "user.email", "test@example.com")
    runGit(t, repo, "config", "user.name", "Test User")

    baseFile := filepath.Join(repo, "foo.txt")
    if err := os.WriteFile(baseFile, []byte("line 1\nline 2\nline 3\n"), 0o644); err != nil {
        t.Fatalf("write base file: %v", err)
    }
    runGit(t, repo, "add", "foo.txt")
    runGit(t, repo, "commit", "-m", "init")

    // Range patch replacing line 2 -> "line two"
    instr := mctpatcher.Instructions{Edits: []mctpatcher.Edit{{
        Path:       "foo.txt",
        Mode:       mctpatcher.ModePatch,
        StartLine:  2,
        EndLine:    2,
        NewContent: "line two\n",
    }}}

    svc := patchersvc.NewService()

    // With FullMode, conversion should succeed
    res, err := svc.ApplyAndGeneratePatch(context.Background(), mctpatcher.PatchParams{
        RepoRoot:     repo,
        SessionID:    "sess",
        Instructions: instr,
        FullMode:     true,
    })
    if err != nil {
        t.Fatalf("ApplyAndGeneratePatch with full-mode: %v", err)
    }
    if res.PatchPath == "" {
        t.Fatalf("expected patch result")
    }
}
