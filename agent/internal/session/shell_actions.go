package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func startShellActionStreamer(bus *ui.EventBus, path string, diagWriter io.Writer) (context.CancelFunc, <-chan struct{}, error) {
	sub, err := listener.New(path)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	opts := listener.SubscribeOptions{
		FollowFromLatest: true,
		Kinds:            []string{"shell-agent.action"},
		ErrorHandler: func(err error) {
			fmt.Fprintf(diagWriter, "[trajectory] shell agent listener error: %v\n", err)
		},
	}
	go func() {
		defer close(done)
		err := sub.Subscribe(ctx, opts, func(ctx context.Context, evt listener.Event) error {
			line := formatShellActionLine(evt.Payload)
			if line != "" {
				bus.Emit(ui.ActionExecutedEvent{Description: line})
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(diagWriter, "[trajectory] shell agent listener stopped: %v\n", err)
		}
	}()
	return cancel, done, nil
}

func formatShellActionLine(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	description := strings.TrimSpace(stringFromAny(payload["description"]))
	if description == "" {
		description = strings.TrimSpace(stringFromAny(payload["command"]))
	}
	if description == "" {
		return ""
	}

	modelCallsUsed, hasModelCallsUsed := intFromAny(payload["model_calls_used"])
	stepLimit, hasStepLimit := intFromAny(payload["step_limit"])
	commandsExecuted, hasCommandsExecuted := intFromAny(payload["commands_executed"])
	if hasModelCallsUsed && hasStepLimit && hasCommandsExecuted && modelCallsUsed >= 0 && stepLimit > 0 && commandsExecuted >= 0 {
		return fmt.Sprintf("[shell step %d/%d cmd %d] %s", modelCallsUsed, stepLimit, commandsExecuted, description)
	}
	return fmt.Sprintf("[shell] %s", description)
}
