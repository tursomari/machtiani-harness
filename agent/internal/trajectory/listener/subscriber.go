package listener

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

const (
	defaultPollInterval       = 50 * time.Millisecond
	defaultBufferSize         = 64
	readChunkSize       int64 = 64 * 1024
)

// Handler receives decoded trajectory events. Returning a non-nil error stops
// the subscription.
type Handler func(context.Context, Event) error

// Filter evaluates whether an event should be delivered to the handler.
type Filter func(Event) bool

// ErrorHandler receives non-fatal errors encountered while tailing.
type ErrorHandler func(error)

// DropHandler observes events dropped due to backpressure.
type DropHandler func(Event)

// SubscribeOptions configure a subscriber invocation.
type SubscribeOptions struct {
	FollowFromLatest bool
	PollInterval     time.Duration
	BufferSize       int
	Kinds            []string
	Components       []string
	CustomFilter     Filter
	ErrorHandler     ErrorHandler
	DropHandler      DropHandler
}

func (o *SubscribeOptions) normalize() {
	if o.PollInterval <= 0 {
		o.PollInterval = defaultPollInterval
	}
	if o.BufferSize <= 0 {
		o.BufferSize = defaultBufferSize
	}
}

// Event represents a decoded trajectory record including metadata not present
// on the emission side.
type Event struct {
	trajectory.Event
	SessionID    string
	Component    string
	Sequence     int64
	Timestamp    time.Time
	RawTimestamp string
}

// Subscriber tails an existing trajectory JSONL file.
type Subscriber struct {
	path string
}

// New creates a subscriber for the provided trajectory file path.
func New(path string) (*Subscriber, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("trajectory/listener: path required")
	}
	return &Subscriber{path: path}, nil
}

// Subscribe streams events to handler until the context is canceled or a fatal
// error occurs.
func (s *Subscriber) Subscribe(ctx context.Context, opts SubscribeOptions, handler Handler) error {
	if s == nil {
		return errors.New("trajectory/listener: subscriber is nil")
	}
	if handler == nil {
		return errors.New("trajectory/listener: handler required")
	}
	opts.normalize()

	kinds := make(map[string]struct{}, len(opts.Kinds))
	for _, k := range opts.Kinds {
		k = strings.TrimSpace(k)
		if k != "" {
			kinds[k] = struct{}{}
		}
	}
	components := make(map[string]struct{}, len(opts.Components))
	for _, c := range opts.Components {
		c = strings.TrimSpace(c)
		if c != "" {
			components[c] = struct{}{}
		}
	}

	matcher := func(evt Event) bool {
		if len(kinds) > 0 {
			if _, ok := kinds[evt.Kind]; !ok {
				return false
			}
		}
		component := evt.Component
		if component == "" {
			component = evt.Event.Component
		}
		if len(components) > 0 {
			if _, ok := components[component]; !ok {
				return false
			}
		}
		if opts.CustomFilter != nil {
			return opts.CustomFilter(evt)
		}
		return true
	}

	f, err := os.Open(s.path)
	if err != nil {
		return fmt.Errorf("trajectory/listener: open file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("trajectory/listener: stat file: %w", err)
	}

	offset := int64(0)
	if opts.FollowFromLatest {
		offset = info.Size()
	}

	eventsCh := make(chan Event, opts.BufferSize)
	handlerErrCh := make(chan error, 1)
	handlerDone := make(chan struct{})

	var handlerOnce sync.Once
	go func() {
		defer close(handlerDone)
		for evt := range eventsCh {
			if err := handler(ctx, evt); err != nil {
				handlerOnce.Do(func() {
					handlerErrCh <- err
				})
				return
			}
		}
	}()

	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()

	buf := make([]byte, readChunkSize)
	pending := make([]byte, 0, readChunkSize)

	emitErr := func(err error) {
		if err == nil {
			return
		}
		if opts.ErrorHandler != nil {
			opts.ErrorHandler(err)
		}
	}

	deliver := func(evt Event) {
		select {
		case eventsCh <- evt:
		default:
			if opts.DropHandler != nil {
				opts.DropHandler(evt)
			}
		}
	}

	readSingle := func() error {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			select {
			case err := <-handlerErrCh:
				return err
			default:
			}

			info, err := f.Stat()
			if err != nil {
				return fmt.Errorf("trajectory/listener: stat file: %w", err)
			}

			size := info.Size()
			if size < offset {
				return fmt.Errorf("trajectory/listener: file truncated (size %d < offset %d)", size, offset)
			}

			if size == offset {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case err := <-handlerErrCh:
					return err
				case <-ticker.C:
					continue
				}
			}

			for offset < size {
				toRead := size - offset
				if toRead > readChunkSize {
					toRead = readChunkSize
				}
				n, err := f.ReadAt(buf[:toRead], offset)
				if err != nil && err != io.EOF {
					return fmt.Errorf("trajectory/listener: read: %w", err)
				}
				if n == 0 {
					break
				}
				offset += int64(n)
				pending = append(pending, buf[:n]...)
				for {
					idx := bytes.IndexByte(pending, '\n')
					if idx == -1 {
						break
					}
					line := pending[:idx]
					pending = pending[idx+1:]
					line = bytes.TrimRight(line, "\r")
					if len(line) == 0 {
						continue
					}
					evt, err := decodeEvent(line)
					if err != nil {
						emitErr(err)
						continue
					}
					if !matcher(evt) {
						continue
					}
					deliver(evt)
				}
				if err == io.EOF {
					break
				}
			}
		}
	}

	readErr := readSingle()

	close(eventsCh)
	<-handlerDone

	if readErr != nil {
		return readErr
	}

	select {
	case err := <-handlerErrCh:
		return err
	default:
	}

	return ctx.Err()
}

func decodeEvent(line []byte) (Event, error) {
	var rec struct {
		Timestamp string `json:"ts"`
		SessionID string `json:"session_id"`
		Component string `json:"component"`
		Sequence  int64  `json:"seq"`
		trajectory.Event
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return Event{}, fmt.Errorf("trajectory/listener: decode event: %w", err)
	}
	ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return Event{}, fmt.Errorf("trajectory/listener: parse timestamp: %w", err)
	}
	evt := Event{
		Event:        rec.Event,
		SessionID:    rec.SessionID,
		Component:    rec.Component,
		Sequence:     rec.Sequence,
		Timestamp:    ts,
		RawTimestamp: rec.Timestamp,
	}
	if evt.Event.Component == "" {
		evt.Event.Component = rec.Component
	}
	return evt, nil
}
