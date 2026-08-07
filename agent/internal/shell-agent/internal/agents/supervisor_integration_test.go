//go:build linux

package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/environments"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestCommandSupervisorStopsRealDaemonProcessGroup(t *testing.T) {
	env, err := environments.NewLocalEnvironment(&minisweagent.EnvironmentConfig{
		CommandTimeout:        10,
		MaxCommandOutputBytes: 65536,
	})
	if err != nil {
		t.Fatalf("NewLocalEnvironment: %v", err)
	}
	defer env.Close()

	model := &singleCommandModel{response: `<command>
sleep 30 & daemon=$!; printf 'daemon_pid=%s\n' "$daemon"; wait "$daemon"
</command>`}
	config := &minisweagent.ShellAgentConfig{
		MaxSteps:                        5,
		FinalizeRemainingSteps:          1,
		CommandSupervisorAfter:          1,
		CommandSupervisorTimeout:        2,
		CommandSupervisorFailureLimit:   2,
		CommandSupervisorMaxSteps:       20,
		CommandSupervisorDeadlineBuffer: 2,
	}
	reviewed := make(chan CommandReviewRequest, 1)
	agent := NewDefaultAgent(model, env, config, &minisweagent.PromptsConfig{}, WithCommandReviewer(func(_ context.Context, request CommandReviewRequest) (CommandReviewResult, error) {
		reviewed <- request
		return CommandReviewResult{Disposition: CommandDispositionCancel, Summary: "test daemon intentionally blocks"}, nil
	}))
	agent.addMessage("user", "exercise the daemon supervisor", nil)

	started := time.Now()
	err = agent.Step(context.Background())
	elapsed := time.Since(started)
	var stopped *minisweagent.ExecutionStoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("Step error = %T %v, want ExecutionStoppedError", err, err)
	}
	if elapsed < 900*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("supervisor stopped command after %s, want approximately 1 second", elapsed)
	}

	request := <-reviewed
	if request.PID <= 0 || request.ProcessGroupID <= 0 {
		t.Fatalf("missing process identity: %+v", request)
	}
	childPID := daemonPIDFromOutput(t, request.Output.Output)
	deadline := time.Now().Add(2 * time.Second)
	for linuxProcessRunning(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if linuxProcessRunning(childPID) {
		t.Fatalf("daemon child %d survived supervisor process-group cancellation", childPID)
	}
}

type singleCommandModel struct {
	response string
	calls    int
}

func (m *singleCommandModel) Config() interface{} { return &minisweagent.ModelConfig{} }
func (m *singleCommandModel) Cost() float64       { return 0 }
func (m *singleCommandModel) NCalls() int         { return m.calls }
func (m *singleCommandModel) Query(context.Context, []minisweagent.Message, ...minisweagent.QueryOption) (minisweagent.QueryResult, error) {
	m.calls++
	return minisweagent.QueryResult{Content: m.response}, nil
}
func (m *singleCommandModel) GetTemplateVars() map[string]interface{} { return nil }

func daemonPIDFromOutput(t *testing.T, output string) int {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		value, ok := strings.CutPrefix(strings.TrimSpace(line), "daemon_pid=")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("parse daemon PID from %q: %v", line, err)
		}
		return pid
	}
	t.Fatalf("daemon PID missing from output %q", output)
	return 0
}

func linuxProcessRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] != "Z"
}
