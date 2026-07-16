package environments

import (
	"context"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

type mockEnvironment struct {
	progress float64
	status   string
}

func (m *mockEnvironment) Config() interface{} {
	return &minisweagent.EnvironmentConfig{}
}

func (m *mockEnvironment) Execute(ctx context.Context, command, cwd string) (minisweagent.ExecuteResult, error) {
	return minisweagent.ExecuteResult{Output: "test output"}, nil
}

func (m *mockEnvironment) GetTemplateVars() map[string]interface{} {
	return map[string]interface{}{"test": "value"}
}

func (m *mockEnvironment) GetSyncProgress() float64 {
	return m.progress
}

func (m *mockEnvironment) GetSyncStatus() string {
	return m.status
}

func TestSyncedEnvironmentDelegatesConfig(t *testing.T) {
	underlying := &mockEnvironment{}
	synced := NewSyncedEnvironment(underlying)

	if synced.Config() == nil {
		t.Fatal("expected config, got nil")
	}
}

func TestSyncedEnvironmentDelegatesExecute(t *testing.T) {
	underlying := &mockEnvironment{}
	synced := NewSyncedEnvironment(underlying)

	result, err := synced.Execute(context.Background(), "test", ".")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "test output" {
		t.Fatalf("got output %q, want %q", result.Output, "test output")
	}
}

func TestSyncedEnvironmentDelegatesTemplateVars(t *testing.T) {
	underlying := &mockEnvironment{}
	synced := NewSyncedEnvironment(underlying)

	vars := synced.GetTemplateVars()
	if vars == nil || vars["test"] != "value" {
		t.Fatalf("expected template vars with test=value, got %v", vars)
	}
}

func TestSyncedEnvironmentDelegatesUnsetProgress(t *testing.T) {
	underlying := &mockEnvironment{progress: 0.5}
	synced := NewSyncedEnvironment(underlying)

	if got := synced.GetSyncProgress(); got != 0.5 {
		t.Fatalf("got progress %f, want 0.5", got)
	}
}

func TestSyncedEnvironmentReturnsSetProgress(t *testing.T) {
	underlying := &mockEnvironment{progress: 0.5}
	synced := NewSyncedEnvironment(underlying)

	synced.SetSyncProgress(0.75)
	if got := synced.GetSyncProgress(); got != 0.75 {
		t.Fatalf("got progress %f, want 0.75", got)
	}
}

func TestSyncedEnvironmentClampsSyncProgress(t *testing.T) {
	underlying := &mockEnvironment{}
	synced := NewSyncedEnvironment(underlying)

	synced.SetSyncProgress(-0.5)
	if got := synced.GetSyncProgress(); got != 0.0 {
		t.Fatalf("got progress %f, want 0.0 (clamped)", got)
	}

	synced.SetSyncProgress(1.5)
	if got := synced.GetSyncProgress(); got != 1.0 {
		t.Fatalf("got progress %f, want 1.0 (clamped)", got)
	}
}

func TestSyncedEnvironmentDelegatesUnsetStatus(t *testing.T) {
	underlying := &mockEnvironment{status: "syncing"}
	synced := NewSyncedEnvironment(underlying)

	if got := synced.GetSyncStatus(); got != "syncing" {
		t.Fatalf("got status %q, want %q", got, "syncing")
	}
}

func TestSyncedEnvironmentReturnsSetStatus(t *testing.T) {
	underlying := &mockEnvironment{status: "syncing"}
	synced := NewSyncedEnvironment(underlying)

	synced.SetSyncStatus("synced 100 files")
	if got := synced.GetSyncStatus(); got != "synced 100 files" {
		t.Fatalf("got status %q, want %q", got, "synced 100 files")
	}
}

func TestSyncedEnvironmentThreadSafety(t *testing.T) {
	underlying := &mockEnvironment{progress: 0.0}
	synced := NewSyncedEnvironment(underlying)

	// Set progress from one goroutine
	done := make(chan struct{})
	go func() {
		synced.SetSyncProgress(0.5)
		synced.SetSyncStatus("halfway")
		close(done)
	}()

	// Read from another goroutine
	go func() {
		_ = synced.GetSyncProgress()
		_ = synced.GetSyncStatus()
	}()

	<-done
	if got := synced.GetSyncProgress(); got != 0.5 {
		t.Fatalf("got progress %f, want 0.5", got)
	}
	if got := synced.GetSyncStatus(); got != "halfway" {
		t.Fatalf("got status %q, want %q", got, "halfway")
	}
}
