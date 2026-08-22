package prompt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shellaction"
	"github.com/tursomari/machtiani/agent/internal/shellbridge"
)

func TestInterceptShellActionPersistsWithoutTrajectoryWriter(t *testing.T) {
	dir := t.TempDir()
	req := shellagent.Request{SessionID: "session-journal", PlannerTurn: 5, TrajectoryBaseDir: dir}
	journal, err := newShellActionJournal(req)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics strings.Builder
	line := shellbridge.ActionPrefix + `{"description":"Run the tests","command":"go test ./...","model_calls_used":3,"step_limit":8,"remaining_steps":5,"commands_executed":2}`
	if !interceptShellAction(context.Background(), line, &diagnostics, journal) {
		t.Fatal("interceptShellAction() = false, want handled")
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("diagnostics = %q", diagnostics.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := shellaction.ParseJournal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	got := records[0]
	if got.SessionID != req.SessionID || got.Turn != req.PlannerTurn || got.Sequence != 1 || got.Step != 3 || got.StepLimit != 8 || got.Command != "go test ./..." {
		t.Fatalf("record = %#v", got)
	}
}

func TestShellActionJournalRecoversSequence(t *testing.T) {
	dir := t.TempDir()
	req := shellagent.Request{SessionID: "session-recover", PlannerTurn: 2, TrajectoryBaseDir: dir}
	first, err := newShellActionJournal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Append(shellbridge.ActionMessage{Command: "first"}); err != nil {
		t.Fatal(err)
	}
	second, err := newShellActionJournal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Append(shellbridge.ActionMessage{Command: "second"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "actions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	records, err := shellaction.ParseJournal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Sequence != 1 || records[1].Sequence != 2 {
		t.Fatalf("sequences = %#v", records)
	}
}
