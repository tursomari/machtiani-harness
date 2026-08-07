package agents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestCommandSupervisorWritesSafeDiagnosticLog(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(900, 0))
	running := newFakeRunningCommand(clock.Now())
	logPath := filepath.Join(t.TempDir(), "command-supervisor.jsonl")
	reviewed := make(chan struct{}, 1)
	agent := commandSupervisorTestAgent(clock, func(context.Context, CommandReviewRequest) (CommandReviewResult, error) {
		reviewed <- struct{}{}
		return CommandReviewResult{Disposition: CommandDispositionContinue, Summary: "sensitive reviewer summary"}, nil
	})
	agent.RunConfig.SessionID = "diagnostic-session"
	agent.RunConfig.CommandSupervisorLogPath = logPath

	done := runCommandWait(agent, running, time.Hour)
	clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
	clock.Advance(10 * time.Second)
	receiveSignal(t, reviewed)
	clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
	running.complete(minisweagent.ExecuteResult{Output: "done", ReturnCode: 0})
	result := receiveCommandWait(t, done)
	if result.err != nil {
		t.Fatalf("wait error = %v", result.err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read supervisor diagnostic log: %v", err)
	}
	for _, secret := range []string{"make test", "working", "sensitive reviewer summary"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("diagnostic log contains sensitive content %q: %s", secret, data)
		}
	}
	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat supervisor diagnostic log: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("diagnostic log mode = %o, want 600", got)
	}

	events := readSupervisorLogEvents(t, logPath)
	wantEvents := []string{"command_started", "review_started", "review_completed", "command_continued", "command_completed"}
	if len(events) != len(wantEvents) {
		t.Fatalf("event count = %d, want %d: %+v", len(events), len(wantEvents), events)
	}
	for i, want := range wantEvents {
		if got := events[i]["event"]; got != want {
			t.Fatalf("event %d = %v, want %q", i, got, want)
		}
	}
	started := events[0]
	if started["session_id"] != "diagnostic-session" || started["command_sha256"] == "" {
		t.Fatalf("command_started metadata = %+v", started)
	}
	if started["pid"] != float64(123) || started["process_group_id"] != float64(123) {
		t.Fatalf("command_started process metadata = %+v", started)
	}
	completed := events[len(events)-1]
	if completed["return_code"] != float64(0) {
		t.Fatalf("command_completed metadata = %+v", completed)
	}
}

func TestCommandSupervisorDiagnosticLogFailureDoesNotAffectCommand(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(950, 0))
	running := newFakeRunningCommand(clock.Now())
	reviewed := make(chan struct{}, 1)
	agent := commandSupervisorTestAgent(clock, func(context.Context, CommandReviewRequest) (CommandReviewResult, error) {
		reviewed <- struct{}{}
		return CommandReviewResult{Disposition: CommandDispositionContinue}, nil
	})
	agent.RunConfig.CommandSupervisorLogPath = t.TempDir() // Opening a directory for append must fail.

	done := runCommandWait(agent, running, time.Hour)
	clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
	clock.Advance(10 * time.Second)
	receiveSignal(t, reviewed)
	clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
	running.complete(minisweagent.ExecuteResult{ReturnCode: 0})
	result := receiveCommandWait(t, done)
	if result.err != nil || result.result.ReturnCode != 0 || running.killCount() != 0 {
		t.Fatalf("logging failure changed command result: %+v, kill count = %d", result, running.killCount())
	}
}

func readSupervisorLogEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read supervisor log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	events := make([]map[string]any, 0, len(lines))
	for index, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse supervisor log line %d: %v: %q", index+1, err, line)
		}
		events = append(events, event)
	}
	return events
}

func TestCommandSupervisorAllowsUnlimitedExplicitContinues(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(1_000, 0))
	running := newFakeRunningCommand(clock.Now())
	reviews := make(chan CommandReviewRequest, 8)
	agent := commandSupervisorTestAgent(clock, func(_ context.Context, request CommandReviewRequest) (CommandReviewResult, error) {
		reviews <- request
		return CommandReviewResult{Disposition: CommandDispositionContinue, Summary: "still making progress"}, nil
	})

	done := runCommandWait(agent, running, time.Hour)
	for round := 1; round <= 7; round++ {
		clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
		clock.Advance(10 * time.Second)
		request := receiveReview(t, reviews)
		if request.ReviewNumber != round {
			t.Fatalf("review number = %d, want %d", request.ReviewNumber, round)
		}
		if request.ConsecutiveFailures != 0 {
			t.Fatalf("explicit continue did not reset failures: %+v", request)
		}
		if running.killCount() != 0 {
			t.Fatalf("command killed after %d explicit continues", round)
		}
	}
	running.complete(minisweagent.ExecuteResult{Output: "done", ReturnCode: 0})
	result := receiveCommandWait(t, done)
	if result.err != nil || result.result.ReturnCode != 0 {
		t.Fatalf("wait result = %+v", result)
	}
}

func TestCommandSupervisorCancelsAfterConsecutiveReviewFailures(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(2_000, 0))
	running := newFakeRunningCommand(clock.Now())
	logPath := filepath.Join(t.TempDir(), "command-supervisor.jsonl")
	called := make(chan struct{}, 2)
	agent := commandSupervisorTestAgent(clock, func(context.Context, CommandReviewRequest) (CommandReviewResult, error) {
		called <- struct{}{}
		return CommandReviewResult{}, errors.New("provider unavailable")
	})
	agent.RunConfig.CommandSupervisorLogPath = logPath
	done := runCommandWait(agent, running, time.Hour)

	for attempt := 1; attempt <= 2; attempt++ {
		clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
		clock.Advance(10 * time.Second)
		receiveSignal(t, called)
		if attempt == 1 {
			clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
			if running.killCount() != 0 {
				t.Fatal("first failed review should provisionally continue")
			}
		}
	}
	result := receiveCommandWait(t, done)
	var stopped *minisweagent.ExecutionStoppedError
	if !errors.As(result.err, &stopped) {
		t.Fatalf("wait error = %T %v, want ExecutionStoppedError", result.err, result.err)
	}
	if running.killCount() != 1 {
		t.Fatalf("kill count = %d, want 1", running.killCount())
	}
	events := readSupervisorLogEvents(t, logPath)
	if got := events[len(events)-2]["event"]; got != "command_stopping" {
		t.Fatalf("penultimate event = %v, want command_stopping", got)
	}
	if got := events[len(events)-1]["event"]; got != "command_stopped" {
		t.Fatalf("last event = %v, want command_stopped", got)
	}
	if got := events[len(events)-1]["stop_reason"]; got != "failure_limit" {
		t.Fatalf("stop reason = %v, want failure_limit", got)
	}
}

func TestCommandSupervisorContinueResetsFailureStreak(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(3_000, 0))
	running := newFakeRunningCommand(clock.Now())
	responses := []struct {
		result CommandReviewResult
		err    error
	}{
		{err: errors.New("first failure")},
		{result: CommandReviewResult{Disposition: CommandDispositionContinue}},
		{err: errors.New("failure after reset")},
		{err: errors.New("consecutive failure")},
	}
	requests := make(chan CommandReviewRequest, len(responses))
	index := 0
	agent := commandSupervisorTestAgent(clock, func(_ context.Context, request CommandReviewRequest) (CommandReviewResult, error) {
		requests <- request
		response := responses[index]
		index++
		return response.result, response.err
	})
	done := runCommandWait(agent, running, time.Hour)

	for i := range responses {
		clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
		clock.Advance(10 * time.Second)
		request := receiveReview(t, requests)
		if i == 2 && request.ConsecutiveFailures != 0 {
			t.Fatalf("continue did not reset failure count before review 3: %+v", request)
		}
	}
	result := receiveCommandWait(t, done)
	if result.err == nil || running.killCount() != 1 {
		t.Fatalf("result = %+v, kill count = %d", result, running.killCount())
	}
}

func TestCommandSupervisorRunsOneUrgentDeadlineReview(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(4_000, 0))
	running := newFakeRunningCommand(clock.Now())
	reviews := make(chan CommandReviewRequest, 4)
	agent := commandSupervisorTestAgent(clock, func(_ context.Context, request CommandReviewRequest) (CommandReviewResult, error) {
		reviews <- request
		return CommandReviewResult{Disposition: CommandDispositionContinue}, nil
	})
	agent.RunConfig.CommandSupervisorAfter = 30
	agent.RunConfig.CommandSupervisorDeadlineBuffer = 20
	done := runCommandWait(agent, running, 100*time.Second)

	for _, advance := range []time.Duration{30 * time.Second, 30 * time.Second, 20 * time.Second} {
		clock.waitForActiveTimer(t, clock.Now().Add(advance))
		clock.Advance(advance)
	}
	first := receiveReview(t, reviews)
	second := receiveReview(t, reviews)
	urgent := receiveReview(t, reviews)
	if first.Reason != CommandReviewRegular || second.Reason != CommandReviewRegular {
		t.Fatalf("regular review reasons = %q, %q", first.Reason, second.Reason)
	}
	if urgent.Reason != CommandReviewDeadline || urgent.Remaining != 20*time.Second {
		t.Fatalf("urgent review = %+v, want exact 20s remaining", urgent)
	}

	clock.waitForActiveTimer(t, clock.Now().Add(20*time.Second))
	clock.Advance(20 * time.Second)
	result := receiveCommandWait(t, done)
	var timeoutErr *minisweagent.ExecutionTimeoutError
	if !errors.As(result.err, &timeoutErr) {
		t.Fatalf("wait error = %T %v, want ExecutionTimeoutError", result.err, result.err)
	}
	select {
	case extra := <-reviews:
		t.Fatalf("unexpected review after urgent deadline review: %+v", extra)
	default:
	}
}

func TestCommandSupervisorTimeoutCountsAsReviewFailure(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(5_000, 0))
	running := newFakeRunningCommand(clock.Now())
	started := make(chan struct{}, 2)
	agent := commandSupervisorTestAgent(clock, func(ctx context.Context, _ CommandReviewRequest) (CommandReviewResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return CommandReviewResult{}, ctx.Err()
	})
	done := runCommandWait(agent, running, time.Hour)

	for attempt := 0; attempt < 2; attempt++ {
		clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
		clock.Advance(10 * time.Second)
		receiveSignal(t, started)
		clock.waitForActiveTimer(t, clock.Now().Add(5*time.Second))
		clock.Advance(5 * time.Second)
	}
	result := receiveCommandWait(t, done)
	if result.err == nil || running.killCount() != 1 {
		t.Fatalf("result = %+v, kill count = %d", result, running.killCount())
	}
}

func TestCommandCompletionDuringReviewNeedsNoDisposition(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(6_000, 0))
	running := newFakeRunningCommand(clock.Now())
	logPath := filepath.Join(t.TempDir(), "command-supervisor.jsonl")
	started := make(chan struct{})
	agent := commandSupervisorTestAgent(clock, func(ctx context.Context, _ CommandReviewRequest) (CommandReviewResult, error) {
		close(started)
		<-ctx.Done()
		return CommandReviewResult{}, ctx.Err()
	})
	agent.RunConfig.CommandSupervisorLogPath = logPath
	done := runCommandWait(agent, running, time.Hour)
	clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
	clock.Advance(10 * time.Second)
	receiveSignal(t, started)
	running.complete(minisweagent.ExecuteResult{Output: "completed during repair", ReturnCode: 0})

	result := receiveCommandWait(t, done)
	if result.err != nil || result.result.ReturnCode != 0 || running.killCount() != 0 {
		t.Fatalf("result = %+v, kill count = %d", result, running.killCount())
	}
	events := readSupervisorLogEvents(t, logPath)
	completed := events[len(events)-1]
	if completed["event"] != "command_completed" || completed["completion_reason"] != "during_review" {
		t.Fatalf("completion event = %+v", completed)
	}
}

func TestCommandDeadlineCannotBeExtendedByActiveReviews(t *testing.T) {
	clock := newFakeCommandClock(time.Unix(7_000, 0))
	running := newFakeRunningCommand(clock.Now())
	logPath := filepath.Join(t.TempDir(), "command-supervisor.jsonl")
	started := make(chan agentsReviewStart, 2)
	agent := commandSupervisorTestAgent(clock, func(ctx context.Context, request CommandReviewRequest) (CommandReviewResult, error) {
		started <- agentsReviewStart{reason: request.Reason, remaining: request.Remaining}
		<-ctx.Done()
		return CommandReviewResult{}, ctx.Err()
	})
	agent.RunConfig.CommandSupervisorAfter = 70
	agent.RunConfig.CommandSupervisorTimeout = 20
	agent.RunConfig.CommandSupervisorFailureLimit = 4
	agent.RunConfig.CommandSupervisorDeadlineBuffer = 20
	agent.RunConfig.CommandSupervisorLogPath = logPath
	done := runCommandWait(agent, running, 100*time.Second)

	clock.waitForActiveTimer(t, clock.Now().Add(70*time.Second))
	clock.Advance(70 * time.Second)
	first := receiveReviewStart(t, started)
	if first.reason != CommandReviewRegular || first.remaining != 30*time.Second {
		t.Fatalf("first review = %+v", first)
	}
	clock.waitForActiveTimer(t, clock.Now().Add(20*time.Second))
	clock.Advance(20 * time.Second)
	urgent := receiveReviewStart(t, started)
	if urgent.reason != CommandReviewDeadline || urgent.remaining != 10*time.Second {
		t.Fatalf("urgent review = %+v", urgent)
	}
	clock.waitForActiveTimer(t, clock.Now().Add(10*time.Second))
	clock.Advance(10 * time.Second)

	result := receiveCommandWait(t, done)
	var timeoutErr *minisweagent.ExecutionTimeoutError
	if !errors.As(result.err, &timeoutErr) {
		t.Fatalf("wait error = %T %v, want hard timeout", result.err, result.err)
	}
	if got := clock.Now(); !got.Equal(running.StartedAt().Add(100 * time.Second)) {
		t.Fatalf("command stopped at %s, want exact deadline", got)
	}
	events := readSupervisorLogEvents(t, logPath)
	stopped := events[len(events)-1]
	if stopped["event"] != "command_stopped" || stopped["stop_reason"] != "hard_timeout" {
		t.Fatalf("deadline stop event = %+v", stopped)
	}
}

type agentsReviewStart struct {
	reason    CommandReviewReason
	remaining time.Duration
}

func receiveReviewStart(t *testing.T, started <-chan agentsReviewStart) agentsReviewStart {
	t.Helper()
	select {
	case review := <-started:
		return review
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for review start")
		return agentsReviewStart{}
	}
}

func commandSupervisorTestAgent(clock CommandClock, reviewer CommandReviewer) *DefaultAgent {
	return &DefaultAgent{RunConfig: &AgentRunConfig{
		Clock:                           clock,
		CommandReviewer:                 reviewer,
		CommandSupervisorAfter:          10,
		CommandSupervisorTimeout:        5,
		CommandSupervisorFailureLimit:   2,
		CommandSupervisorDeadlineBuffer: 20,
	}}
}

type commandWaitResult struct {
	result minisweagent.ExecuteResult
	err    error
}

func runCommandWait(agent *DefaultAgent, running minisweagent.RunningCommand, timeout time.Duration) <-chan commandWaitResult {
	done := make(chan commandWaitResult, 1)
	go func() {
		result, err := agent.waitForRunningCommand(context.Background(), "make test", 3, running, timeout)
		done <- commandWaitResult{result: result, err: err}
	}()
	return done
}

func receiveCommandWait(t *testing.T, done <-chan commandWaitResult) commandWaitResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for command supervisor")
		return commandWaitResult{}
	}
}

func receiveReview(t *testing.T, reviews <-chan CommandReviewRequest) CommandReviewRequest {
	t.Helper()
	select {
	case review := <-reviews:
		return review
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for command review")
		return CommandReviewRequest{}
	}
}

func receiveSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

type fakeRunningCommand struct {
	started time.Time
	done    chan struct{}
	once    sync.Once
	mu      sync.Mutex
	result  minisweagent.ExecuteResult
	kills   int
}

func newFakeRunningCommand(started time.Time) *fakeRunningCommand {
	return &fakeRunningCommand{started: started, done: make(chan struct{})}
}

func (c *fakeRunningCommand) PID() int             { return 123 }
func (c *fakeRunningCommand) ProcessGroupID() int  { return 123 }
func (c *fakeRunningCommand) StartedAt() time.Time { return c.started }
func (c *fakeRunningCommand) Snapshot() minisweagent.CommandOutputSnapshot {
	return minisweagent.CommandOutputSnapshot{Output: "working", CapturedBytes: 7, TotalBytes: 7, UpdatedAt: c.started}
}
func (c *fakeRunningCommand) Done() <-chan struct{} { return c.done }
func (c *fakeRunningCommand) Wait() (minisweagent.ExecuteResult, error) {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result, nil
}
func (c *fakeRunningCommand) Kill() error {
	c.mu.Lock()
	c.kills++
	c.mu.Unlock()
	c.complete(minisweagent.ExecuteResult{ReturnCode: -1})
	return nil
}
func (c *fakeRunningCommand) complete(result minisweagent.ExecuteResult) {
	c.once.Do(func() {
		c.mu.Lock()
		c.result = result
		c.mu.Unlock()
		close(c.done)
	})
}
func (c *fakeRunningCommand) killCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kills
}

type fakeCommandClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeCommandTimer
	changed chan struct{}
}

func newFakeCommandClock(now time.Time) *fakeCommandClock {
	return &fakeCommandClock{now: now, changed: make(chan struct{}, 64)}
}

func (c *fakeCommandClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeCommandClock) NewTimer(duration time.Duration) CommandTimer {
	c.mu.Lock()
	now := c.now
	timer := &fakeCommandTimer{at: now.Add(duration), channel: make(chan time.Time, 1)}
	c.timers = append(c.timers, timer)
	c.mu.Unlock()
	if duration <= 0 {
		timer.fire(now)
	}
	select {
	case c.changed <- struct{}{}:
	default:
	}
	return timer
}

func (c *fakeCommandClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	now := c.now
	timers := append([]*fakeCommandTimer(nil), c.timers...)
	c.mu.Unlock()
	for _, timer := range timers {
		timer.fire(now)
	}
}

func (c *fakeCommandClock) waitForActiveTimer(t *testing.T, at time.Time) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		c.mu.Lock()
		found := false
		for _, timer := range c.timers {
			if timer.activeAt(at) {
				found = true
				break
			}
		}
		c.mu.Unlock()
		if found {
			return
		}
		select {
		case <-c.changed:
		case <-deadline:
			t.Fatalf("timed out waiting for active timer at %s", at)
		}
	}
}

type fakeCommandTimer struct {
	mu      sync.Mutex
	at      time.Time
	channel chan time.Time
	stopped bool
	fired   bool
}

func (t *fakeCommandTimer) C() <-chan time.Time { return t.channel }
func (t *fakeCommandTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := !t.stopped && !t.fired
	t.stopped = true
	return wasActive
}
func (t *fakeCommandTimer) fire(now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped || t.fired || now.Before(t.at) {
		return
	}
	t.fired = true
	t.channel <- now
}
func (t *fakeCommandTimer) activeAt(at time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return !t.stopped && !t.fired && t.at.Equal(at)
}
