package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

var (
	defaultCommandSupervisorLogPath = commandSupervisorDefaultLogPath()
	commandSupervisorLogMu          sync.Mutex
)

func commandSupervisorDefaultLogPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.TempDir(), "mct-command-supervisor.jsonl")
	}
	return "/tmp/mct-command-supervisor.jsonl"
}

// commandSupervisorLogEvent is intentionally metadata-only. Commands, output,
// prompts, reviewer summaries, and error messages may contain credentials or
// other sensitive data and must not be copied into this temporary diagnostic.
type commandSupervisorLogEvent struct {
	Timestamp                  time.Time           `json:"timestamp"`
	Event                      string              `json:"event"`
	SessionID                  string              `json:"session_id,omitempty"`
	AgentPID                   int                 `json:"agent_pid"`
	CommandNumber              int                 `json:"command_number"`
	CommandSHA256              string              `json:"command_sha256"`
	PID                        int                 `json:"pid"`
	ProcessGroupID             int                 `json:"process_group_id"`
	StartedAt                  time.Time           `json:"started_at"`
	Deadline                   time.Time           `json:"deadline"`
	RemainingMilliseconds      int64               `json:"remaining_ms"`
	CommandTimeoutMilliseconds int64               `json:"command_timeout_ms,omitempty"`
	ReviewAfterMilliseconds    int64               `json:"review_after_ms,omitempty"`
	ReviewTimeoutMilliseconds  int64               `json:"review_timeout_ms,omitempty"`
	DeadlineBufferMilliseconds int64               `json:"deadline_buffer_ms,omitempty"`
	FailureLimit               int                 `json:"failure_limit,omitempty"`
	ReviewNumber               int                 `json:"review_number,omitempty"`
	Reason                     CommandReviewReason `json:"reason,omitempty"`
	ConsecutiveFailures        int                 `json:"consecutive_failures,omitempty"`
	Disposition                CommandDisposition  `json:"disposition,omitempty"`
	ReviewDurationMilliseconds int64               `json:"review_duration_ms,omitempty"`
	CapturedOutputBytes        int64               `json:"captured_output_bytes,omitempty"`
	TotalOutputBytes           int64               `json:"total_output_bytes,omitempty"`
	OutputUpdatedAt            *time.Time          `json:"output_updated_at,omitempty"`
	ErrorKind                  string              `json:"error_kind,omitempty"`
	StopReason                 string              `json:"stop_reason,omitempty"`
	CompletionReason           string              `json:"completion_reason,omitempty"`
	ReturnCode                 *int                `json:"return_code,omitempty"`
}

type commandSupervisorTrace struct {
	agent          *DefaultAgent
	clock          CommandClock
	commandNumber  int
	commandSHA256  string
	running        minisweagent.RunningCommand
	deadline       time.Time
	commandTimeout time.Duration
	reviewAfter    time.Duration
	reviewTimeout  time.Duration
	deadlineBuffer time.Duration
	failureLimit   int
}

func newCommandSupervisorTrace(
	agent *DefaultAgent,
	clock CommandClock,
	command string,
	commandNumber int,
	running minisweagent.RunningCommand,
	deadline time.Time,
	commandTimeout time.Duration,
	reviewAfter time.Duration,
	reviewTimeout time.Duration,
	deadlineBuffer time.Duration,
	failureLimit int,
) *commandSupervisorTrace {
	sum := sha256.Sum256([]byte(command))
	return &commandSupervisorTrace{
		agent:          agent,
		clock:          clock,
		commandNumber:  commandNumber,
		commandSHA256:  hex.EncodeToString(sum[:]),
		running:        running,
		deadline:       deadline,
		commandTimeout: commandTimeout,
		reviewAfter:    reviewAfter,
		reviewTimeout:  reviewTimeout,
		deadlineBuffer: deadlineBuffer,
		failureLimit:   failureLimit,
	}
}

func (t *commandSupervisorTrace) emit(event string, configure ...func(*commandSupervisorLogEvent)) {
	if t == nil || t.agent == nil || t.agent.RunConfig == nil || t.agent.RunConfig.CommandSupervisorLogPath == "" {
		return
	}
	now := t.clock.Now()
	remaining := t.deadline.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	record := commandSupervisorLogEvent{
		Timestamp:             now.UTC(),
		Event:                 event,
		SessionID:             t.agent.RunConfig.SessionID,
		AgentPID:              os.Getpid(),
		CommandNumber:         t.commandNumber,
		CommandSHA256:         t.commandSHA256,
		PID:                   t.running.PID(),
		ProcessGroupID:        t.running.ProcessGroupID(),
		StartedAt:             t.running.StartedAt().UTC(),
		Deadline:              t.deadline.UTC(),
		RemainingMilliseconds: remaining.Milliseconds(),
	}
	if event == "command_started" {
		record.CommandTimeoutMilliseconds = t.commandTimeout.Milliseconds()
		record.ReviewAfterMilliseconds = t.reviewAfter.Milliseconds()
		record.ReviewTimeoutMilliseconds = t.reviewTimeout.Milliseconds()
		record.DeadlineBufferMilliseconds = t.deadlineBuffer.Milliseconds()
		record.FailureLimit = t.failureLimit
	}
	for _, apply := range configure {
		if apply != nil {
			apply(&record)
		}
	}
	_ = appendCommandSupervisorLog(t.agent.RunConfig.CommandSupervisorLogPath, record)
}

func (t *commandSupervisorTrace) completed(result minisweagent.ExecuteResult, err error, reason string) {
	t.emit("command_completed", func(event *commandSupervisorLogEvent) {
		event.ReturnCode = &result.ReturnCode
		event.ErrorKind = commandSupervisorErrorKind(err)
		event.CompletionReason = reason
	})
}

func (t *commandSupervisorTrace) stop(running minisweagent.RunningCommand, reason error, stopReason string) (minisweagent.ExecuteResult, error) {
	t.emit("command_stopping", func(event *commandSupervisorLogEvent) {
		event.StopReason = stopReason
		event.ErrorKind = commandSupervisorErrorKind(reason)
	})
	result, err := stopRunningCommand(running, reason)
	t.emit("command_stopped", func(event *commandSupervisorLogEvent) {
		event.StopReason = stopReason
		event.ReturnCode = &result.ReturnCode
		event.ErrorKind = commandSupervisorErrorKind(err)
	})
	return result, err
}

func appendCommandSupervisorLog(path string, event commandSupervisorLogEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	commandSupervisorLogMu.Lock()
	defer commandSupervisorLogMu.Unlock()
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("command supervisor log path is not a regular file")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	_, err = file.Write(payload)
	return err
}

func commandSupervisorErrorKind(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	default:
		return fmt.Sprintf("%T", err)
	}
}
