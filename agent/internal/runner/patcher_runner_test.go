package runner

import (
	"context"
	"testing"

	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

type stubService struct {
	lastParams mctpatcher.PatchParams
	result     *mctpatcher.PatchResult
	err        error
}

func (s *stubService) ApplyAndGeneratePatch(_ context.Context, params mctpatcher.PatchParams) (*mctpatcher.PatchResult, error) {
	s.lastParams = params
	if s.err != nil {
		return nil, s.err
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
	pr.ApplyFunc = func(string, string, bool) error { return nil }
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
}

func TestApplyInvokesService(t *testing.T) {
	stub := &stubService{result: &mctpatcher.PatchResult{PatchPath: "ok.patch"}}
	workspaceDir := t.TempDir()
	mirrorDir := t.TempDir()
	pr := &PatcherRunner{
		Enabled:          true,
		SessionID:        "sess",
		Service:          stub,
		Verbose:          true,
		RepoRoot:         t.TempDir(),
		WorkspaceFactory: func(string) (string, func(), error) { return workspaceDir, func() {}, nil },
		MirrorFactory:    func() (string, func(), error) { return mirrorDir, func() {}, nil },
		ApplyFunc:        func(string, string, bool) error { return nil },
	}
	if err := pr.Resolve(); err != nil {
		t.Fatalf("resolve error: %v", err)
	}
	instr := mctpatcher.Instructions{Edits: []mctpatcher.Edit{{Path: "a.txt", Mode: mctpatcher.ModeCreate, NewContent: "hi"}}}
	res, err := pr.Apply(context.Background(), instr, false)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.PatchPath != "ok.patch" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if stub.lastParams.SessionID != "sess" {
		t.Fatalf("session id not passed to service: %+v", stub.lastParams)
	}
	if !stub.lastParams.Verbose {
		t.Fatalf("expected verbose to propagate to service")
	}
	if stub.lastParams.WorkspaceRoot != workspaceDir {
		t.Fatalf("expected workspace root to be passed; got %s", stub.lastParams.WorkspaceRoot)
	}
	if stub.lastParams.MirrorDir != mirrorDir {
		t.Fatalf("expected mirror dir to be passed; got %s", stub.lastParams.MirrorDir)
	}
	if stub.lastParams.Sequence != 1 {
		t.Fatalf("expected sequence 1, got %d", stub.lastParams.Sequence)
	}
}

func TestApplyDisabled(t *testing.T) {
	pr := &PatcherRunner{}
	if _, err := pr.Apply(context.Background(), mctpatcher.Instructions{}, false); err == nil {
		t.Fatalf("expected error when runner disabled")
	}
}

func TestFinalizeUsesSequenceOrder(t *testing.T) {
	pr := &PatcherRunner{Enabled: true, SessionID: "sess", RepoRoot: t.TempDir()}
	pr.repoAbs = pr.RepoRoot
	pr.applied = []patchLogEntry{
		{Path: "b.patch", Order: 2},
		{Path: "a.patch", Order: 1},
	}
	var calls []string
	pr.ApplyFunc = func(_ string, patch string, _ bool) error {
		calls = append(calls, patch)
		return nil
	}
	if err := pr.Finalize(false); err != nil {
		t.Fatalf("finalize error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 apply calls, got %d", len(calls))
	}
	if calls[0] != "a.patch" || calls[1] != "b.patch" {
		t.Fatalf("patches applied out of order: %v", calls)
	}
	for _, entry := range pr.applied {
		if !entry.Finalized {
			t.Fatalf("expected entry %+v finalized", entry)
		}
	}
}
