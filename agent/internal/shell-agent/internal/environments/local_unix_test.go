//go:build !windows

package environments

import (
	"context"
	"fmt"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunningCommandKillStopsEntireProcessGroup(t *testing.T) {
	repo := initTestRepo(t)
	env, err := NewLocalEnvironment(&minisweagent.EnvironmentConfig{CommandTimeout: 5})
	if err != nil {
		t.Fatalf("NewLocalEnvironment: %v", err)
	}

	running, err := env.Start(context.Background(), "sleep 30 & child=$!; printf '%s\\n' \"$child\"; wait", repo)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	output := waitForOutput(t, running, "\n")
	childPID, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		t.Fatalf("parse child PID from %q: %v", output, err)
	}
	if childPGID, err := syscall.Getpgid(childPID); err != nil || childPGID != running.ProcessGroupID() {
		t.Fatalf("child PGID = %d, %v; want %d", childPGID, err, running.ProcessGroupID())
	}

	if err := running.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := running.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processRunning(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processRunning(childPID) {
		t.Fatalf("child process %d still running after process-group kill", childPID)
	}
}

func processRunning(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data))
	return len(fields) > 2 && fields[2] != "Z"
}
