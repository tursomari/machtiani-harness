package agents

import (
	"context"
	"fmt"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// CommandClock and CommandTimer isolate all supervisor scheduling so policy
// tests can advance time without sleeping.
type CommandClock interface {
	Now() time.Time
	NewTimer(time.Duration) CommandTimer
}

type CommandTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type realCommandClock struct{}

func (realCommandClock) Now() time.Time { return time.Now() }
func (realCommandClock) NewTimer(duration time.Duration) CommandTimer {
	return &realCommandTimer{Timer: time.NewTimer(duration)}
}

type realCommandTimer struct{ *time.Timer }

func (t *realCommandTimer) C() <-chan time.Time { return t.Timer.C }

func (a *DefaultAgent) commandClock() CommandClock {
	if a != nil && a.RunConfig != nil && a.RunConfig.Clock != nil {
		return a.RunConfig.Clock
	}
	return realCommandClock{}
}

func (a *DefaultAgent) waitForRunningCommand(
	ctx context.Context,
	command string,
	commandNumber int,
	running minisweagent.RunningCommand,
	timeout time.Duration,
) (minisweagent.ExecuteResult, error) {
	clock := a.commandClock()
	deadline := running.StartedAt().Add(timeout)
	after := time.Duration(a.RunConfig.CommandSupervisorAfter) * time.Second
	reviewTimeout := time.Duration(a.RunConfig.CommandSupervisorTimeout) * time.Second
	deadlineBuffer := time.Duration(a.RunConfig.CommandSupervisorDeadlineBuffer) * time.Second
	reviewsEnabled := after > 0 && reviewTimeout > 0 && a.RunConfig.CommandReviewer != nil

	var nextReview time.Time
	var urgentAt time.Time
	if reviewsEnabled {
		nextReview = running.StartedAt().Add(after)
		urgentAt = deadline.Add(-deadlineBuffer)
	}
	urgentDone := false
	consecutiveFailures := 0
	reviewNumber := 0

	for {
		now := clock.Now()
		if !now.Before(deadline) {
			return stopRunningCommand(running, &minisweagent.ExecutionTimeoutError{Message: fmt.Sprintf("command timed out after %s", timeout)})
		}

		eventAt := deadline
		reason := CommandReviewReason("")
		if reviewsEnabled && !urgentDone {
			if !urgentAt.After(now) && urgentAt.Before(eventAt) {
				eventAt = now
				reason = CommandReviewDeadline
			} else if urgentAt.Before(eventAt) || urgentAt.Equal(eventAt) {
				eventAt = urgentAt
				reason = CommandReviewDeadline
			}
			if !nextReview.IsZero() && nextReview.Before(eventAt) {
				eventAt = nextReview
				reason = CommandReviewRegular
			}
		}

		wait := eventAt.Sub(now)
		if wait < 0 {
			wait = 0
		}
		timer := clock.NewTimer(wait)
		select {
		case <-running.Done():
			timer.Stop()
			return running.Wait()
		case <-ctx.Done():
			timer.Stop()
			return stopRunningCommand(running, ctx.Err())
		case <-timer.C():
		}

		now = clock.Now()
		if !now.Before(deadline) || reason == "" {
			return stopRunningCommand(running, &minisweagent.ExecutionTimeoutError{Message: fmt.Sprintf("command timed out after %s", timeout)})
		}
		if reason == CommandReviewDeadline {
			urgentDone = true
			nextReview = time.Time{}
		} else if reason == CommandReviewRegular {
			// Consume this periodic event before the potentially long review.
			// A subsequent review is scheduled only after its disposition.
			nextReview = time.Time{}
		}

		reviewNumber++
		remaining := deadline.Sub(now)
		if remaining < 0 {
			remaining = 0
		}
		request := CommandReviewRequest{
			Command:             command,
			CommandNumber:       commandNumber,
			ReviewNumber:        reviewNumber,
			Reason:              reason,
			ConsecutiveFailures: consecutiveFailures,
			PID:                 running.PID(),
			ProcessGroupID:      running.ProcessGroupID(),
			StartedAt:           running.StartedAt(),
			Deadline:            deadline,
			Remaining:           remaining,
			Output:              running.Snapshot(),
		}
		effectiveReviewTimeout := min(reviewTimeout, remaining)
		decision, reviewErr, completed := a.performCommandReview(ctx, running, request, effectiveReviewTimeout)
		if completed {
			return running.Wait()
		}
		if ctx.Err() != nil {
			return stopRunningCommand(running, ctx.Err())
		}
		if reviewErr == nil && decision.Disposition != CommandDispositionContinue && decision.Disposition != CommandDispositionCancel {
			reviewErr = fmt.Errorf("invalid command supervisor disposition %q", decision.Disposition)
		}
		if reviewErr != nil {
			consecutiveFailures++
			if consecutiveFailures >= a.RunConfig.CommandSupervisorFailureLimit {
				return stopRunningCommand(running, &minisweagent.ExecutionStoppedError{Message: fmt.Sprintf("command stopped after %d consecutive supervisor failures: %v", consecutiveFailures, reviewErr)})
			}
			if !urgentDone {
				nextReview = clock.Now().Add(after)
			}
			continue
		}
		if decision.Disposition == CommandDispositionCancel {
			message := "command stopped by supervisor"
			if decision.Summary != "" {
				message += ": " + decision.Summary
			}
			return stopRunningCommand(running, &minisweagent.ExecutionStoppedError{Message: message})
		}

		consecutiveFailures = 0
		if !urgentDone {
			nextReview = clock.Now().Add(after)
		}
	}
}

func (a *DefaultAgent) performCommandReview(
	ctx context.Context,
	running minisweagent.RunningCommand,
	request CommandReviewRequest,
	timeout time.Duration,
) (CommandReviewResult, error, bool) {
	reviewCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		result CommandReviewResult
		err    error
	}
	resultCh := make(chan outcome, 1)
	go func() {
		result, err := a.RunConfig.CommandReviewer(reviewCtx, request)
		resultCh <- outcome{result: result, err: err}
	}()
	timer := a.commandClock().NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-running.Done():
		return CommandReviewResult{}, nil, true
	case <-ctx.Done():
		return CommandReviewResult{}, ctx.Err(), false
	case <-timer.C():
		return CommandReviewResult{}, context.DeadlineExceeded, false
	case result := <-resultCh:
		select {
		case <-running.Done():
			return CommandReviewResult{}, nil, true
		default:
		}
		return result.result, result.err, false
	}
}

func stopRunningCommand(running minisweagent.RunningCommand, reason error) (minisweagent.ExecuteResult, error) {
	if err := running.Kill(); err != nil {
		return minisweagent.ExecuteResult{}, fmt.Errorf("stop command: %w", err)
	}
	result, err := running.Wait()
	if err != nil {
		return result, err
	}
	return result, reason
}
