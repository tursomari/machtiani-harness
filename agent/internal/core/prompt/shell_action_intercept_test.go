package prompt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"github.com/tursomari/machtiani/agent/internal/shellaction"
	"github.com/tursomari/machtiani/agent/internal/shellbridge"
)

func TestObserveShellActionPersistsWithoutTrajectoryWriter(t *testing.T) {
	dir := t.TempDir()
	req := shellagent.Request{SessionID: "session-journal", PlannerTurn: 5, TrajectoryBaseDir: dir}
	journal, err := newShellActionJournal(req)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics strings.Builder
	observeShellAction(shellbridge.ActionMessage{
		Description: "Run the tests", Command: "go test ./...", ModelCallsUsed: 3,
		StepLimit: 8, RemainingSteps: 5, CommandsExecuted: 2,
	}, &diagnostics, journal)
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
	if got.SessionID != req.SessionID || got.Turn != req.PlannerTurn || got.Sequence != 1 || got.Step != 2 || got.StepLimit != 8 || got.Command != "go test ./..." {
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
	path := filepath.Join(dir, "actions.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"v":1,"seq":999`); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := newShellActionJournal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Append(shellbridge.ActionMessage{Command: "second"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
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

func TestRunShellAgentPersistsActionBeforeCommandWithoutTrajectoryWriter(t *testing.T) {
	dir := t.TempDir()
	actionsPath := filepath.Join(dir, "actions.jsonl")
	model := &journalIntegrationModel{}
	environment := &journalIntegrationEnvironment{actionsPath: actionsPath}
	req := shellagent.Request{
		PreconstructedMessages: []llm.Message{
			{Role: "system", Content: "You are a shell agent."},
			{Role: "user", Content: "Run one command and finish."},
		},
		Task:              "Run one command and finish.",
		Config:            &minisweagent.ShellAgentConfig{},
		Prompts:           &minisweagent.PromptsConfig{},
		Model:             model,
		Env:               environment,
		SessionID:         "session-integration-journal",
		PlannerTurn:       7,
		ResumeAttempt:     false,
		TrajectoryBaseDir: dir,
	}

	result, err := runShellAgentWithInterception(context.Background(), req)
	if err != nil {
		t.Fatalf("runShellAgentWithInterception() error = %v", err)
	}
	if result.ExitStatus != "Submitted" {
		t.Fatalf("exit status = %q, want Submitted (result error: %v, model calls: %d, observed: %t)", result.ExitStatus, result.Error, model.calls, environment.observedBeforeExecute)
	}
	if !environment.observedBeforeExecute {
		t.Fatal("environment did not observe the action journal before command execution")
	}
}

type journalIntegrationModel struct {
	calls int
}

func (m *journalIntegrationModel) Config() interface{} { return &minisweagent.ModelConfig{} }
func (m *journalIntegrationModel) Cost() float64       { return 0 }
func (m *journalIntegrationModel) NCalls() int         { return m.calls }
func (m *journalIntegrationModel) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}
func (m *journalIntegrationModel) Query(_ context.Context, _ []minisweagent.Message, _ ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.calls++
	if m.calls == 1 {
		return minisweagent.QueryResult{Content: "<command>printf journal-ready</command>"}, nil
	}
	return minisweagent.QueryResult{Content: "<answer>\nConfidence: 100% - journal persisted before execution\n</answer>"}, nil
}

type journalIntegrationEnvironment struct {
	actionsPath           string
	observedBeforeExecute bool
}

func (e *journalIntegrationEnvironment) Config() interface{} {
	return &minisweagent.EnvironmentConfig{}
}
func (e *journalIntegrationEnvironment) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{}
}
func (e *journalIntegrationEnvironment) GetSyncProgress() float64 { return 1 }
func (e *journalIntegrationEnvironment) GetSyncStatus() string    { return "" }
func (e *journalIntegrationEnvironment) Execute(_ context.Context, command, _ string) (minisweagent.ExecuteResult, error) {
	data, err := os.ReadFile(e.actionsPath)
	if err != nil {
		return minisweagent.ExecuteResult{}, fmt.Errorf("read action before execute: %w", err)
	}
	records, err := shellaction.ParseJournal(data)
	if err != nil {
		return minisweagent.ExecuteResult{}, err
	}
	if len(records) != 1 || records[0].Command != command {
		return minisweagent.ExecuteResult{}, fmt.Errorf("action records before execute = %#v", records)
	}
	e.observedBeforeExecute = true
	return minisweagent.ExecuteResult{Output: "journal-ready", ReturnCode: 0}, nil
}
