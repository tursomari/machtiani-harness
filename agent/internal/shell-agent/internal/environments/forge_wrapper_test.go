package environments

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// fakeEnvironment implements minisweagent.Environment for testing.
type fakeEnvironment struct {
	lastCommand string
	lastCwd     string
	lastCtx     context.Context
	output      string
	returnCode  int
	execErr     error
}

func (f *fakeEnvironment) Config() interface{}                     { return nil }
func (f *fakeEnvironment) GetTemplateVars() map[string]interface{} { return nil }
func (f *fakeEnvironment) GetSyncProgress() float64                { return 0 }
func (f *fakeEnvironment) GetSyncStatus() string                   { return "" }

func (f *fakeEnvironment) Execute(ctx context.Context, command, cwd string) (minisweagent.ExecuteResult, error) {
	f.lastCtx = ctx
	f.lastCommand = command
	f.lastCwd = cwd
	if f.execErr != nil {
		return minisweagent.ExecuteResult{Output: f.output, ReturnCode: f.returnCode}, f.execErr
	}
	return minisweagent.ExecuteResult{Output: f.output, ReturnCode: f.returnCode}, nil
}

func TestForgeWrapper_PassThrough(t *testing.T) {
	fake := &fakeEnvironment{
		output:     "file1.go\nfile2.go\n",
		returnCode: 0,
	}
	wrapper := NewForgeWrapper(fake)

	result, err := wrapper.Execute(context.Background(), "ls *.go", "/home/user/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "file1.go\nfile2.go\n" {
		t.Fatalf("got output %q, want %q", result.Output, "file1.go\nfile2.go\n")
	}
	if result.ReturnCode != 0 {
		t.Fatalf("got ReturnCode %d, want 0", result.ReturnCode)
	}
	if result.Metadata != "" {
		t.Fatalf("got Metadata %q, want empty", result.Metadata)
	}
	if fake.lastCommand != "ls *.go" {
		t.Fatalf("got lastCommand %q, want %q", fake.lastCommand, "ls *.go")
	}
	if fake.lastCwd != "/home/user/project" {
		t.Fatalf("got lastCwd %q, want %q", fake.lastCwd, "/home/user/project")
	}
}

func TestForgeWrapper_MisuseDetection(t *testing.T) {
	fake := &fakeEnvironment{}
	wrapper := NewForgeWrapper(fake)

	tests := []struct {
		name    string
		command string
	}{
		{"pipe to machtiani-forge", "echo 'hello' | machtiani-forge"},
		{"xargs machtiani-forge", "echo 'hello' | xargs machtiani-forge"},
		{"arbitrary command before machtiani-forge", "make test && machtiani-forge 'fix the failure'"},
		{"semicolon before machtiani-forge", "cd /tmp; machtiani-forge 'fix the failure'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake.lastCommand = "" // reset between subtests
			result, err := wrapper.Execute(context.Background(), tt.command, "/tmp")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.ReturnCode != 1 {
				t.Fatalf("got ReturnCode %d, want 1", result.ReturnCode)
			}
			if !strings.Contains(result.Output, "machtiani-forge error") {
				t.Fatalf("got Output %q, want it to contain 'machtiani-forge error'", result.Output)
			}
			if !strings.Contains(result.Output, "must be the first command") {
				t.Fatalf("got Output %q, want it to contain 'must be the first command'", result.Output)
			}
			if fake.lastCommand != "" {
				t.Fatalf("inner environment was called unexpectedly with command %q", fake.lastCommand)
			}
		})
	}
}

func TestForgeWrapper_MisuseDetection_PathNotFlagged(t *testing.T) {
	fake := &fakeEnvironment{
		output:     "ok",
		returnCode: 0,
	}
	wrapper := NewForgeWrapper(fake)

	tests := []struct {
		name    string
		command string
	}{
		{"cat with forge in path", "cat /home/user/machtiani-forge/readme.txt"},
		{"grep forge pattern", "grep machtiani-forge somefile.txt"},
		{"ls with forge in path", "ls /path/to/machtiani-forge/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake.lastCommand = "" // reset between subtests
			result, err := wrapper.Execute(context.Background(), tt.command, "/tmp")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result.ReturnCode != 0 {
				t.Fatalf("got ReturnCode %d, want 0", result.ReturnCode)
			}
			if fake.lastCommand != tt.command {
				t.Fatalf("got lastCommand %q, want %q", fake.lastCommand, tt.command)
			}
		})
	}
}

func TestForgeWrapper_SuccessfulForgeCall(t *testing.T) {
	note := "'add a comment to main.go'"

	fake := &fakeEnvironment{
		output:     "Forge applied patch successfully.\nfile changed: main.go\n",
		returnCode: 0,
	}
	wrapper := NewForgeWrapper(fake)

	// Use a cancelled parent context to verify the inner environment
	// receives a context without a deadline (ctx.Err() == nil).
	parentCtx, cancel := context.WithCancel(context.Background())
	cancel()

	command := "machtiani-forge 'add a comment to main.go'"
	result, err := wrapper.Execute(parentCtx, command, "/home/user/project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the inner environment received a context without a deadline.
	if fake.lastCtx == nil {
		t.Fatal("inner environment did not receive a context")
	}
	if err := fake.lastCtx.Err(); err != nil {
		t.Fatalf("inner context should have no deadline, but got error: %v", err)
	}

	// Verify the inner environment received the exact same command unchanged (pass-through).
	if fake.lastCommand != command {
		t.Fatalf("got lastCommand %q, want %q", fake.lastCommand, command)
	}
	if fake.lastCwd != "/home/user/project" {
		t.Fatalf("got lastCwd %q, want %q", fake.lastCwd, "/home/user/project")
	}

	// Output must match the inner response.
	if result.Output != fake.output {
		t.Fatalf("got Output %q, want %q", result.Output, fake.output)
	}

	// Metadata must be valid JSON with the required keys.
	if result.Metadata == "" {
		t.Fatal("Metadata is empty")
	}

	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(result.Metadata), &meta); err != nil {
		t.Fatalf("failed to unmarshal Metadata as JSON: %v\nraw: %s", err, result.Metadata)
	}

	if action, ok := meta["action"].(string); !ok || action != "forge" {
		t.Fatalf("got action %v, want 'forge'", meta["action"])
	}

	exitCode, ok := meta["exit_code"].(float64)
	if !ok {
		t.Fatalf("exit_code is missing or not a number: %v", meta["exit_code"])
	}
	if int(exitCode) != fake.returnCode {
		t.Fatalf("got exit_code %v, want %d", exitCode, fake.returnCode)
	}

	durationMS, ok := meta["duration_ms"].(float64)
	if !ok {
		t.Fatalf("duration_ms is missing or not a number: %v", meta["duration_ms"])
	}
	if durationMS < 0 {
		t.Fatalf("duration_ms %v is negative", durationMS)
	}

	noteBytes, ok := meta["note_bytes"].(float64)
	if !ok {
		t.Fatalf("note_bytes is missing or not a number: %v", meta["note_bytes"])
	}
	if int(noteBytes) != len(note) {
		t.Fatalf("got note_bytes %v, want %d", noteBytes, len(note))
	}

	outputBytes, ok := meta["output_bytes"].(float64)
	if !ok {
		t.Fatalf("output_bytes is missing or not a number: %v", meta["output_bytes"])
	}
	if int(outputBytes) != len(fake.output) {
		t.Fatalf("got output_bytes %v, want %d", outputBytes, len(fake.output))
	}
}

func TestForgeWrapper_InnerError(t *testing.T) {
	innerErr := fmt.Errorf("connection refused")
	fake := &fakeEnvironment{
		output:     "partial output before crash",
		returnCode: 2,
		execErr:    innerErr,
	}
	wrapper := NewForgeWrapper(fake)

	command := "machtiani-forge 'fix the bug'"
	_, err := wrapper.Execute(context.Background(), command, "/home/user/project")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if err.Error() != innerErr.Error() {
		t.Fatalf("got error %q, want %q", err.Error(), innerErr.Error())
	}
}
