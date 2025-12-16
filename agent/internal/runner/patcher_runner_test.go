package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/gitops"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
)

type stubService struct {
    lastParams mctpatcher.PatchParams
    result     *mctpatcher.PatchResult
    err        error
    hook       func(params mctpatcher.PatchParams) error
}

func (s *stubService) ApplyAndGeneratePatch(_ context.Context, params mctpatcher.PatchParams) (*mctpatcher.PatchResult, error) {
	s.lastParams = params
	if s.err != nil {
		return nil, s.err
	}
	if s.hook != nil {
		if err := s.hook(params); err != nil {
			return nil, err
		}
	}
	if s.result != nil {
		return s.result, nil
	}
	return &mctpatcher.PatchResult{PatchPath: "stub.patch"}, nil
}

func (s *stubService) ValidateInstructions(context.Context, string, mctpatcher.Instructions) error {
	return nil
}

func TestResolveRequiresSession(t *testing.T) {
	pr := &PatcherRunner{Enabled: true}
	if err := pr.Resolve(); err == nil {
		t.Fatalf("expected error when session id missing")
	}
}

func TestResolveInstantiatesService(t *testing.T) {
	pr := &PatcherRunner{Enabled: true, SessionID: "sess", RepoRoot: t.TempDir()}
	pr.WorkspaceFactory = func(string) (string, func(), error) {
		return t.TempDir(), func() {}, nil
	}
	pr.MirrorFactory = func() (string, func(), error) {
		return t.TempDir(), func() {}, nil
	}
	if err := pr.Resolve(); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if pr.Service == nil {
		t.Fatalf("expected service to be created")
	}
	if pr.workspaceDir == "" {
		t.Fatalf("expected workspace to be initialized")
	}
	if pr.mirrorDir == "" {
		t.Fatalf("expected mirror to be initialized")
	}
}

func TestApplyDryRun(t *testing.T) {
	pr := &PatcherRunner{Enabled: true, DryRun: true, SessionID: "sess"}
	instr := mctpatcher.Instructions{Edits: []mctpatcher.Edit{{Path: "a.txt", Mode: mctpatcher.ModeCreate, NewContent: "hi"}}}
	res, err := pr.Apply(context.Background(), instr, false)
	if err != nil {
		t.Fatalf("dry-run apply returned error: %v", err)
	}
	if res == nil {
		t.Fatalf("expected result in dry-run")
	}
	if res.Sequence != 1 {
		t.Fatalf("expected sequence 1, got %d", res.Sequence)
	}
	if res.ReversePatchPath != "" {
		t.Fatalf("expected no reverse patch path during dry-run")
	}
}

func TestApplyPerformsAtomicVerification(t *testing.T) {
	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoRoot, "foo.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runGit(t, repoRoot, "add", "foo.txt")
	runGit(t, repoRoot, "commit", "-m", "init")

	if err := os.WriteFile(filepath.Join(repoRoot, "notes.tmp"), []byte("scratch"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	patchDir := t.TempDir()
	patchPath := filepath.Join(patchDir, "change.patch")
	patchContent := "diff --git a/foo.txt b/foo.txt\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/foo.txt\n" +
		"+++ b/foo.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"
	if err := os.WriteFile(patchPath, []byte(patchContent), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}

    stub := &stubService{
        result: &mctpatcher.PatchResult{
            PatchPath:          patchPath,
            Sequence:           0,
            FilesModified:      []string{"foo.txt"},
            AppliedInWorkspace: true,
        },
    }
    stub.hook = func(params mctpatcher.PatchParams) error {
        if params.WorkspaceRoot == "" {
            return nil
        }
        return gitops.ApplyPatchInDir(params.WorkspaceRoot, patchPath, false)
    }
	stub.hook = func(params mctpatcher.PatchParams) error {
		if params.WorkspaceRoot == "" {
			return nil
		}
		return gitops.ApplyPatchInDir(params.WorkspaceRoot, patchPath, false)
	}

	var workspaceDir string
    pr := &PatcherRunner{
        Enabled:   true,
        SessionID: "sess",
        Service:   stub,
        RepoRoot:  repoRoot,
        FullMode:  true,
        WorkspaceFactory: func(root string) (string, func(), error) {
            ws, cleanup, err := patchersvc.CreateWorkspace(root)
            if err == nil {
                workspaceDir = ws
            }
            return ws, cleanup, err
        },
        MirrorFactory: func() (string, func(), error) {
            return t.TempDir(), func() {}, nil
        },
    }
	if err := pr.Resolve(); err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if workspaceDir == "" {
		t.Fatalf("workspace not initialized")
	}

	instr := mctpatcher.Instructions{Edits: []mctpatcher.Edit{{Path: "foo.txt", Mode: mctpatcher.ModeRewrite, NewContent: "new\n"}}}
    res, err := pr.Apply(context.Background(), instr, false)
    if err != nil {
        t.Fatalf("apply error: %v", err)
    }
	if res == nil {
		t.Fatalf("expected patch result")
	}
	if res.PatchPath != patchPath {
		t.Fatalf("unexpected patch path: %s", res.PatchPath)
	}
	if res.Sequence != 1 {
		t.Fatalf("expected sequence 1, got %d", res.Sequence)
	}
	if res.ReversePatchPath == "" {
		t.Fatalf("expected reverse patch path to be set")
	}
	if _, err := os.Stat(res.ReversePatchPath); err != nil {
		t.Fatalf("reverse patch not written: %v", err)
	}

	workspaceContent, err := os.ReadFile(filepath.Join(workspaceDir, "foo.txt"))
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if string(workspaceContent) != "new\n" {
		t.Fatalf("workspace not updated after apply: %q", workspaceContent)
	}

	// In snapshot-first mode, syncing the snapshot repo to the host is handled by
	// the session runner, not the patcher runner.

	if len(pr.applied) != 1 {
		t.Fatalf("expected 1 logged patch, got %d", len(pr.applied))
	}
	if pr.applied[0].ReversePath != res.ReversePatchPath {
		t.Fatalf("reverse path not tracked in log")
	}
	if pr.applied[0].Finalized {
		t.Fatalf("expected patch not marked finalized after apply")
	}
	if stub.lastParams.SessionID != "sess" {
		t.Fatalf("session id not propagated: %+v", stub.lastParams)
	}
	if stub.lastParams.WorkspaceRoot != workspaceDir {
		t.Fatalf("workspace root not passed to service")
	}
	if stub.lastParams.MirrorDir != pr.mirrorDir {
		t.Fatalf("mirror dir not passed to service")
	}
    if stub.lastParams.Sequence != 1 {
        t.Fatalf("expected sequence 1 in params, got %d", stub.lastParams.Sequence)
    }
    if !stub.lastParams.FullMode {
        t.Fatalf("expected FullMode=true in params, got false")
    }
}

func TestRunnerPassesFullModeParam(t *testing.T) {
    repo := t.TempDir()
    runGit(t, repo, "init")
    runGit(t, repo, "config", "user.email", "test@example.com")
    runGit(t, repo, "config", "user.name", "Test User")
    if err := os.WriteFile(filepath.Join(repo, "foo.txt"), []byte("old\n"), 0o644); err != nil { t.Fatalf("write base file: %v", err) }
    runGit(t, repo, "add", "foo.txt")
    runGit(t, repo, "commit", "-m", "init")

    patchDir := t.TempDir()
    patchPath := filepath.Join(patchDir, "change.patch")
    patchContent := "diff --git a/foo.txt b/foo.txt\n" +
        "index 1111111..2222222 100644\n" +
        "--- a/foo.txt\n" +
        "+++ b/foo.txt\n" +
        "@@ -1 +1 @@\n" +
        "-old\n" +
        "+new\n"
    if err := os.WriteFile(patchPath, []byte(patchContent), 0o644); err != nil { t.Fatalf("write patch: %v", err) }

    pr := &PatcherRunner{Enabled: true, SessionID: "sess", RepoRoot: repo, FullMode: true}
    pr.WorkspaceFactory = func(root string) (string, func(), error) { return patchersvc.CreateWorkspace(root) }
    pr.MirrorFactory = func() (string, func(), error) { return t.TempDir(), func() {}, nil }
    stub := &stubService{result: &mctpatcher.PatchResult{PatchPath: patchPath, FilesModified: []string{"foo.txt"}, AppliedInWorkspace: true}}
    stub.hook = func(params mctpatcher.PatchParams) error {
        if params.WorkspaceRoot == "" { return nil }
        return gitops.ApplyPatchInDir(params.WorkspaceRoot, patchPath, false)
    }
    pr.Service = stub
    if err := pr.Resolve(); err != nil { t.Fatalf("resolve: %v", err) }
    instr := mctpatcher.Instructions{Edits: []mctpatcher.Edit{{Path: "foo.txt", Mode: mctpatcher.ModeRewrite, NewContent: "new\n"}}}
    if _, err := pr.Apply(context.Background(), instr, false); err != nil { t.Fatalf("apply: %v", err) }
    if !stub.lastParams.FullMode { t.Fatalf("expected FullMode propagated, got false") }
}

func TestUndoRevertsPatch(t *testing.T) {
	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoRoot, "foo.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runGit(t, repoRoot, "add", "foo.txt")
	runGit(t, repoRoot, "commit", "-m", "init")

	patchDir := t.TempDir()
	patchPath := filepath.Join(patchDir, "change.patch")
	patchContent := "diff --git a/foo.txt b/foo.txt\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/foo.txt\n" +
		"+++ b/foo.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+new\n"
	if err := os.WriteFile(patchPath, []byte(patchContent), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}

	stub := &stubService{
		result: &mctpatcher.PatchResult{
			PatchPath:          patchPath,
			Sequence:           1,
			FilesModified:      []string{"foo.txt"},
			AppliedInWorkspace: true,
		},
	}
	stub.hook = func(params mctpatcher.PatchParams) error {
		if params.WorkspaceRoot == "" {
			return nil
		}
		return gitops.ApplyPatchInDir(params.WorkspaceRoot, patchPath, false)
	}

	var workspaceDir string
    pr := &PatcherRunner{
        Enabled:   true,
        SessionID: "sess",
        Service:   stub,
        RepoRoot:  repoRoot,
        FullMode:  true,
        WorkspaceFactory: func(root string) (string, func(), error) {
            ws, cleanup, err := patchersvc.CreateWorkspace(root)
            if err == nil {
                workspaceDir = ws
            }
			return ws, cleanup, err
		},
		MirrorFactory: func() (string, func(), error) {
			return t.TempDir(), func() {}, nil
		},
	}
	if err := pr.Resolve(); err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if workspaceDir == "" {
		t.Fatalf("workspace not initialized")
	}

	instr := mctpatcher.Instructions{Edits: []mctpatcher.Edit{{Path: "foo.txt", Mode: mctpatcher.ModeRewrite, NewContent: "new\n"}}}
	res, err := pr.Apply(context.Background(), instr, false)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.ReversePatchPath == "" {
		t.Fatalf("expected reverse patch path")
	}

	content, err := os.ReadFile(filepath.Join(workspaceDir, "foo.txt"))
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if string(content) != "new\n" {
		t.Fatalf("expected workspace to contain new content before undo, got %q", content)
	}

	if err := pr.Undo(res.ReversePatchPath); err != nil {
		t.Fatalf("undo error: %v", err)
	}

	workspaceContent, err := os.ReadFile(filepath.Join(workspaceDir, "foo.txt"))
	if err != nil {
		t.Fatalf("read workspace file: %v", err)
	}
	if string(workspaceContent) != "old\n" {
		t.Fatalf("expected workspace to revert to original content, got %q", workspaceContent)
	}
	if len(pr.applied) == 0 || pr.applied[0].Finalized {
		t.Fatalf("expected patch log to reflect undo")
	}
}

func TestApplyAtomicVerificationAllowsBaselineDirty(t *testing.T) {
	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoRoot, "foo.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write base file: %v", err)
	}
	runGit(t, repoRoot, "add", "foo.txt")
	runGit(t, repoRoot, "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(repoRoot, "notes.tmp"), []byte("scratch"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	patchDir := t.TempDir()
	patchPath := filepath.Join(patchDir, "change.patch")
	patchContent := "diff --git a/foo.txt b/foo.txt\n" +
		"index 1111111..3333333 100644\n" +
		"--- a/foo.txt\n" +
		"+++ b/foo.txt\n" +
		"@@ -1 +1 @@\n" +
		"-old\n" +
		"+newer\n"
	if err := os.WriteFile(patchPath, []byte(patchContent), 0o644); err != nil {
		t.Fatalf("write patch: %v", err)
	}

	stub := &stubService{
		result: &mctpatcher.PatchResult{
			PatchPath:          patchPath,
			Sequence:           0,
			FilesModified:      []string{"foo.txt"},
			AppliedInWorkspace: true,
		},
	}
	stub.hook = func(params mctpatcher.PatchParams) error {
		if params.WorkspaceRoot == "" {
			return nil
		}
		return gitops.ApplyPatchInDir(params.WorkspaceRoot, patchPath, false)
	}

	var workspaceDir string
	pr := &PatcherRunner{
		Enabled:   true,
		SessionID: "sess",
		Service:   stub,
		RepoRoot:  repoRoot,
		WorkspaceFactory: func(root string) (string, func(), error) {
			ws, cleanup, err := patchersvc.CreateWorkspace(root)
			if err == nil {
				workspaceDir = ws
			}
			return ws, cleanup, err
		},
		MirrorFactory: func() (string, func(), error) {
			return t.TempDir(), func() {}, nil
		},
	}
	if err := pr.Resolve(); err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	if workspaceDir == "" {
		t.Fatalf("workspace not initialized")
	}

	instr := mctpatcher.Instructions{Edits: []mctpatcher.Edit{{Path: "foo.txt", Mode: mctpatcher.ModeRewrite, NewContent: "newer\n"}}}
	if _, err := pr.Apply(context.Background(), instr, false); err != nil {
		t.Fatalf("apply error: %v", err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v (%s)", args, err, string(out))
	}
}
