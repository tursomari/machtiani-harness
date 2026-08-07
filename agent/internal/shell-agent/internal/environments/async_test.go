package environments

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

type asyncTestEnvironment struct {
	startCtx     context.Context
	startCommand string
	startCWD     string
	running      minisweagent.RunningCommand
}

func (e *asyncTestEnvironment) Config() interface{} { return &minisweagent.EnvironmentConfig{} }
func (e *asyncTestEnvironment) Execute(context.Context, string, string) (minisweagent.ExecuteResult, error) {
	return minisweagent.ExecuteResult{}, nil
}
func (e *asyncTestEnvironment) Start(ctx context.Context, command, cwd string) (minisweagent.RunningCommand, error) {
	e.startCtx = ctx
	e.startCommand = command
	e.startCWD = cwd
	return e.running, nil
}
func (e *asyncTestEnvironment) GetTemplateVars() map[string]interface{} { return nil }
func (e *asyncTestEnvironment) GetSyncProgress() float64                { return 1 }
func (e *asyncTestEnvironment) GetSyncStatus() string                   { return "" }

type asyncTestCommand struct {
	result minisweagent.ExecuteResult
	done   chan struct{}
}

func newAsyncTestCommand(result minisweagent.ExecuteResult) *asyncTestCommand {
	done := make(chan struct{})
	close(done)
	return &asyncTestCommand{result: result, done: done}
}

func (c *asyncTestCommand) PID() int             { return 101 }
func (c *asyncTestCommand) ProcessGroupID() int  { return 101 }
func (c *asyncTestCommand) StartedAt() time.Time { return time.Unix(1, 0) }
func (c *asyncTestCommand) Snapshot() minisweagent.CommandOutputSnapshot {
	return minisweagent.CommandOutputSnapshot{Output: c.result.Output}
}
func (c *asyncTestCommand) Done() <-chan struct{} { return c.done }
func (c *asyncTestCommand) Wait() (minisweagent.ExecuteResult, error) {
	return c.result, nil
}
func (c *asyncTestCommand) Kill() error { return nil }

func TestSyncedEnvironmentForwardsRunningCommand(t *testing.T) {
	running := newAsyncTestCommand(minisweagent.ExecuteResult{Output: "ok"})
	inner := &asyncTestEnvironment{running: running}
	env := NewSyncedEnvironment(inner)

	got, err := env.Start(context.Background(), "make test", "/repo")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got != running || inner.startCommand != "make test" || inner.startCWD != "/repo" {
		t.Fatalf("running command was not forwarded: got=%v command=%q cwd=%q", got, inner.startCommand, inner.startCWD)
	}
}

func TestForgeWrapperForwardsRunningCommand(t *testing.T) {
	running := newAsyncTestCommand(minisweagent.ExecuteResult{Output: "ok"})
	inner := &asyncTestEnvironment{running: running}
	env := NewForgeWrapper(inner)

	got, err := env.Start(context.Background(), "make test", "/repo")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got != running || inner.startCommand != "make test" {
		t.Fatalf("running command was not forwarded: got=%v command=%q", got, inner.startCommand)
	}
}

func TestForgeWrapperAsyncCommandRetainsMetadataAndLaunchContext(t *testing.T) {
	running := newAsyncTestCommand(minisweagent.ExecuteResult{Output: "forged", ReturnCode: 0})
	inner := &asyncTestEnvironment{running: running}
	env := NewForgeWrapper(inner)
	parent, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := env.Start(parent, "mct-forge 'finish this'", "/repo")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := inner.startCtx.Err(); err != nil {
		t.Fatalf("forge launch context should ignore parent cancellation: %v", err)
	}
	result, err := got.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	var metadata forgeMetadata
	if err := json.Unmarshal([]byte(result.Metadata), &metadata); err != nil {
		t.Fatalf("metadata %q: %v", result.Metadata, err)
	}
	if metadata.Action != "forge" || metadata.NoteBytes == 0 || metadata.OutputBytes != len("forged") {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}

var (
	_ minisweagent.AsyncEnvironment = (*LocalEnvironment)(nil)
	_ minisweagent.AsyncEnvironment = (*SyncedEnvironment)(nil)
	_ minisweagent.AsyncEnvironment = (*ForgeWrapper)(nil)
)
