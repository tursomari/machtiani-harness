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
			action, ok := shellActionEventFromPayload(evt.Payload)
			if ok {
				bus.Emit(action)
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(diagWriter, "[trajectory] shell agent listener stopped: %v\n", err)
		}
	}()
	return cancel, done, nil
}

func shellActionEventFromPayload(payload map[string]any) (ui.ActionExecutedEvent, bool) {
	if payload == nil {
		return ui.ActionExecutedEvent{}, false
	}
	command := strings.TrimSpace(stringFromAny(payload["command"]))
	description := ui.CleanActionDescription(stringFromAny(payload["description"]))
	if description == "" {
		description = command
	}
	if description == "" && command == "" {
		return ui.ActionExecutedEvent{}, false
	}

	action := ui.ActionExecutedEvent{
		Command:     command,
		Description: description,
	}
	if stepLimit, ok := intFromAny(payload["step_limit"]); ok && stepLimit >= 0 {
		action.StepLimit = stepLimit
	}
	if remainingSteps, ok := intFromAny(payload["remaining_steps"]); ok && remainingSteps >= 0 {
		action.RemainingSteps = remainingSteps
	}
	if commandsExecuted, ok := intFromAny(payload["commands_executed"]); ok && commandsExecuted >= 0 {
		action.CommandsExecuted = commandsExecuted
		action.Step = commandsExecuted
	}
	return action, true
}
