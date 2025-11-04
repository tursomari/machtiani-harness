package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func startShellActionStreamer(display *ui.TerminalDisplay, path string) (context.CancelFunc, <-chan struct{}, error) {
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
			fmt.Fprintf(os.Stderr, "[trajectory] shell agent listener error: %v\n", err)
		},
	}
	go func() {
		defer close(done)
		err := sub.Subscribe(ctx, opts, func(ctx context.Context, evt listener.Event) error {
			description := extractShellActionDescription(evt.Payload)
			if description != "" {
				display.StreamAction(description)
			}
			return nil
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(os.Stderr, "[trajectory] shell agent listener stopped: %v\n", err)
		}
	}()
	return cancel, done, nil
}

func extractShellActionDescription(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	if desc := strings.TrimSpace(stringFromAny(payload["description"])); desc != "" {
		return desc
	}
	if cmd := strings.TrimSpace(stringFromAny(payload["command"])); cmd != "" {
		return cmd
	}
	return ""
}
