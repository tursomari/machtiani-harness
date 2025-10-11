package runner

import (
	"context"
	"testing"

	mctpatcher "github.com/tursomari/machtiani/mct/patcher"
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
	pr := &PatcherRunner{Enabled: true, SessionID: "sess"}
	if err := pr.Resolve(); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if pr.Service == nil {
		t.Fatalf("expected service to be created")
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
}

func TestApplyInvokesService(t *testing.T) {
	stub := &stubService{result: &mctpatcher.PatchResult{PatchPath: "ok.patch"}}
	pr := &PatcherRunner{Enabled: true, SessionID: "sess", Service: stub, Verbose: true}
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
}

func TestApplyDisabled(t *testing.T) {
	pr := &PatcherRunner{}
	if _, err := pr.Apply(context.Background(), mctpatcher.Instructions{}, false); err == nil {
		t.Fatalf("expected error when runner disabled")
	}
}
