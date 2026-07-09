package listener

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

func TestSubscriberStreamsEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.jsonl")

	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})

	sub, err := New(path)
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const total = 4

	var (
		mu  sync.Mutex
		got []Event
	)

	errCh := make(chan error, 1)

	go func() {
		errCh <- sub.Subscribe(ctx, SubscribeOptions{PollInterval: 5 * time.Millisecond, BufferSize: 8}, func(ctx context.Context, evt Event) error {
			mu.Lock()
			got = append(got, evt)
			if len(got) == total {
				cancel()
			}
			mu.Unlock()
			return nil
		})
	}()

	time.Sleep(10 * time.Millisecond)

	for i := 0; i < total; i++ {
		payload := map[string]any{"event_version": 1, "step": i}
		if err := writer.Emit(context.Background(), trajectory.Event{Kind: "agent.turn.progress", Payload: payload}); err != nil {
			t.Fatalf("emit event %d: %v", i, err)
		}
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for subscriber")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(got) != total {
		t.Fatalf("expected %d events, got %d", total, len(got))
	}

	for i, evt := range got {
		if evt.SessionID != "sess" {
			t.Fatalf("event %d session mismatch: %q", i, evt.SessionID)
		}
		if evt.Component != "agent" {
			t.Fatalf("event %d component mismatch: %q", i, evt.Component)
		}
		if evt.Sequence != int64(i+1) {
			t.Fatalf("event %d sequence mismatch: got %d want %d", i, evt.Sequence, i+1)
		}
		if evt.Timestamp.IsZero() {
			t.Fatalf("event %d timestamp is zero", i)
		}
		val, ok := evt.Payload["step"].(float64)
		if !ok || int(val) != i {
			t.Fatalf("event %d payload step mismatch: %+v", i, evt.Payload)
		}
	}
}

func TestSubscriberFiltersEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.jsonl")

	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})

	sub, err := New(path)
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts := SubscribeOptions{
		PollInterval: 5 * time.Millisecond,
		BufferSize:   8,
		Kinds:        []string{"planner.request", "planner.response"},
		Components:   []string{"planner"},
		CustomFilter: func(evt Event) bool {
			accepted, _ := evt.Payload["accepted"].(bool)
			return evt.Kind == "planner.response" && accepted
		},
	}

	var (
		mu  sync.Mutex
		got []Event
	)

	errCh := make(chan error, 1)

	go func() {
		errCh <- sub.Subscribe(ctx, opts, func(ctx context.Context, evt Event) error {
			mu.Lock()
			got = append(got, evt)
			if len(got) == 1 {
				cancel()
			}
			mu.Unlock()
			return nil
		})
	}()

	time.Sleep(10 * time.Millisecond)

	emit := func(kind string, component string, payload map[string]any) {
		payload["event_version"] = 1
		if err := writer.Emit(context.Background(), trajectory.Event{Kind: kind, Component: component, Payload: payload}); err != nil {
			t.Fatalf("emit %s: %v", kind, err)
		}
	}

	emit("agent.turn.start", "", map[string]any{"step": 1})
	emit("planner.request", "planner", map[string]any{"prompt": "hi"})
	emit("planner.response", "planner", map[string]any{"accepted": false})
	emit("planner.response", "planner", map[string]any{"accepted": true})

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for subscriber")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("expected 1 event, got %d", len(got))
	}
	if got[0].Kind != "planner.response" {
		t.Fatalf("unexpected event kind: %s", got[0].Kind)
	}
	accepted, _ := got[0].Payload["accepted"].(bool)
	if !accepted {
		t.Fatalf("expected accepted response")
	}
}

func TestSubscriberBackpressureAndDrop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.jsonl")

	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})

	sub, err := New(path)
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var (
		mu        sync.Mutex
		sequences []int64
		dropCount atomic.Int64
	)
	done := make(chan struct{})

	opts := SubscribeOptions{
		PollInterval: 5 * time.Millisecond,
		BufferSize:   1,
		DropHandler: func(Event) {
			dropCount.Add(1)
		},
	}

	errCh := make(chan error, 1)

	go func() {
		errCh <- sub.Subscribe(ctx, opts, func(ctx context.Context, evt Event) error {
			mu.Lock()
			sequences = append(sequences, evt.Sequence)
			if evt.Sequence == 5 {
				close(done)
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			return nil
		})
	}()

	time.Sleep(10 * time.Millisecond)

	emit := func(seq int) {
		payload := map[string]any{"event_version": 1, "seq": seq}
		if err := writer.Emit(context.Background(), trajectory.Event{Kind: "agent.turn.progress", Payload: payload}); err != nil {
			t.Fatalf("emit %d: %v", seq, err)
		}
	}

	for i := 0; i < 4; i++ {
		emit(i)
	}

	time.Sleep(120 * time.Millisecond)

	emit(4)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for final event")
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for subscriber")
	}

	if dropCount.Load() == 0 {
		t.Fatalf("expected drop handler to record drops")
	}

	mu.Lock()
	defer mu.Unlock()

	if len(sequences) == 0 {
		t.Fatalf("no events processed")
	}
	last := sequences[len(sequences)-1]
	if last != 5 {
		t.Fatalf("expected to process final event sequence 5, got %d (all sequences: %v)", last, sequences)
	}
}

func TestSubscriberHandlesMissingFile(t *testing.T) {
	sub, err := New("/tmp/non-existent-path.jsonl")
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = sub.Subscribe(ctx, SubscribeOptions{}, func(ctx context.Context, evt Event) error {
		return nil
	})
	if err == nil {
		t.Fatalf("expected error when subscribing to missing file")
	}
	if !strings.Contains(err.Error(), "open file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSubscriberRecoversFromTruncation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.jsonl")

	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})

	sub, err := New(path)
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	seen := make(chan struct{}, 1)
	truncated := make(chan struct{}, 1)
	secondSeen := make(chan struct{}, 1)

	go func() {
		errCh <- sub.Subscribe(ctx, SubscribeOptions{
			PollInterval: 5 * time.Millisecond,
			ErrorHandler: func(err error) {
				if strings.Contains(err.Error(), "file truncated") {
					select {
					case truncated <- struct{}{}:
					default:
					}
				}
			},
		}, func(ctx context.Context, evt Event) error {
			if evt.Kind == "agent.turn.after-truncate" {
				select {
				case secondSeen <- struct{}{}:
				default:
				}
				cancel()
				return nil
			}
			select {
			case seen <- struct{}{}:
			default:
			}
			return nil
		})
	}()

	time.Sleep(10 * time.Millisecond)

	payload := map[string]any{"event_version": 1}
	if err := writer.Emit(context.Background(), trajectory.Event{Kind: "agent.turn.progress", Payload: payload}); err != nil {
		t.Fatalf("emit: %v", err)
	}

	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for first event")
	}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	select {
	case <-truncated:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for truncation notification")
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close first writer: %v", err)
	}
	writer2, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("new second writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer2.Close()
	})
	if err := writer2.Emit(context.Background(), trajectory.Event{Kind: "agent.turn.after-truncate", Payload: map[string]any{"event_version": 1}}); err != nil {
		t.Fatalf("emit after truncate: %v", err)
	}

	select {
	case <-secondSeen:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for event after truncation")
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for subscriber")
	}
}

func TestSubscriberSkipsMalformedJSONLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.jsonl")

	if err := os.WriteFile(path, []byte("definitely not json\n"), 0o644); err != nil {
		t.Fatalf("write malformed line: %v", err)
	}

	sub, err := New(path)
	if err != nil {
		t.Fatalf("new subscriber: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	decodeErr := make(chan struct{}, 1)
	seen := make(chan struct{}, 1)
	errCh := make(chan error, 1)

	go func() {
		errCh <- sub.Subscribe(ctx, SubscribeOptions{
			PollInterval: 5 * time.Millisecond,
			ErrorHandler: func(err error) {
				if strings.Contains(err.Error(), "decode event") {
					select {
					case decodeErr <- struct{}{}:
					default:
					}
				}
			},
		}, func(ctx context.Context, evt Event) error {
			select {
			case seen <- struct{}{}:
			default:
			}
			cancel()
			return nil
		})
	}()

	select {
	case <-decodeErr:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for decode error notification")
	}

	writer, err := trajectory.New(trajectory.Config{SessionID: "sess", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() {
		_ = writer.Close()
	})
	if err := writer.Emit(context.Background(), trajectory.Event{Kind: "agent.turn.valid", Payload: map[string]any{"event_version": 1}}); err != nil {
		t.Fatalf("emit valid event: %v", err)
	}

	select {
	case <-seen:
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for valid event after malformed line")
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout waiting for subscriber")
	}
}
