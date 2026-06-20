package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func startLLMRetryLogger(display ui.SessionDisplay, path string, diagWriter io.Writer) (context.CancelFunc, <-chan struct{}, error) {
	sub, err := listener.New(path)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	opts := listener.SubscribeOptions{
		FollowFromLatest: true,
		Kinds:            []string{"llm.retry"},
		ErrorHandler: func(err error) {
			fmt.Fprintf(diagWriter, "[trajectory] retry listener error: %v\n", err)
		},
	}
	go func() {
		defer close(done)
		err := sub.Subscribe(ctx, opts, func(ctx context.Context, evt listener.Event) error {
			if msg := formatLLMRetryEvent(evt); strings.TrimSpace(msg) != "" {
				display.Notify(msg)
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(diagWriter, "[trajectory] retry listener stopped: %v\n", err)
		}
	}()
	return cancel, done, nil
}

func formatLLMRetryEvent(evt listener.Event) string {
	payload := evt.Payload
	if payload == nil {
		return ""
	}
	modelLabel := strings.TrimSpace(describeFailoverModel(payload["model"]))
	if modelLabel == "" || modelLabel == "unknown model" {
		modelLabel = strings.TrimSpace(stringFromAny(payload["alias"]))
	}
	mode := strings.TrimSpace(stringFromAny(payload["mode"]))
	attempt, _ := intFromAny(payload["attempt"])
	nextAttempt, _ := intFromAny(payload["next_attempt"])
	waitMs, _ := intFromAny(payload["wait_ms"])
	status, hasStatus := intFromAny(payload["status"])
	reason := strings.TrimSpace(errorMessageFromEvent(evt))

	msg := "[llm retry]"
	if modelLabel != "" {
		msg += " " + modelLabel
	}
	if mode != "" {
		msg += fmt.Sprintf(" (%s)", mode)
	}

	details := make([]string, 0, 4)
	if attempt > 0 {
		details = append(details, fmt.Sprintf("attempt %d failed", attempt))
	}
	if nextAttempt > 0 {
		details = append(details, fmt.Sprintf("retrying attempt %d", nextAttempt))
	}
	if waitMs > 0 {
		details = append(details, fmt.Sprintf("in %s", (time.Duration(waitMs)*time.Millisecond).String()))
	}
	if hasStatus && status > 0 {
		statusText := strings.TrimSpace(http.StatusText(status))
		if statusText != "" {
			details = append(details, fmt.Sprintf("status %d %s", status, statusText))
		} else {
			details = append(details, fmt.Sprintf("status %d", status))
		}
	}
	if len(details) > 0 {
		msg += ": " + strings.Join(details, "; ")
	}
	if reason != "" && !(hasStatus && status > 0) {
		if len(details) == 0 {
			msg += ": " + reason
		} else {
			msg += " (" + reason + ")"
		}
	}
	return msg
}
