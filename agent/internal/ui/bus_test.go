package ui

import (
	"sync"
	"testing"
	"time"
)

func TestEventBus_Ordering(t *testing.T) {
	bus := NewEventBus(10)
	ch := bus.Subscribe()

	bus.Emit(SessionStartedEvent{Goal: "test"})
	bus.Emit(NotificationEvent{Level: NotificationInfo, Message: "hello"})
	bus.Emit(SessionEndedEvent{})

	bus.Unsubscribe(ch)

	expected := []string{"SessionStarted", "Notification", "SessionEnded"}
	for i, want := range expected {
		select {
		case evt := <-ch:
			if got := evt.Type(); got != want {
				t.Errorf("event %d: expected Type %q, got %q", i, want, got)
			}
		default:
			t.Fatalf("event %d: expected %q but channel was empty", i, want)
		}
	}

	// Verify channel is empty after receiving all 3.
	select {
	case evt := <-ch:
		t.Errorf("channel not empty: unexpected event %v", evt)
	default:
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus(10)
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()
	ch3 := bus.Subscribe()
	bus.Emit(SessionStartedEvent{Goal: "test"})

	channels := []<-chan DisplayEvent{ch1, ch2, ch3}
	for i, ch := range channels {
		select {
		case evt := <-ch:
			if evt.Type() != "SessionStarted" {
				t.Errorf("subscriber %d: expected Type %q, got %q", i, "SessionStarted", evt.Type())
			}
		default:
			t.Errorf("subscriber %d: expected one event but channel was empty", i)
		}
	}
	bus.Close()
}

func TestEventBus_Backpressure(t *testing.T) {
	bus := NewEventBus(1)
	ch := bus.Subscribe()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		bus.Emit(SessionStartedEvent{Goal: "first"})
		bus.Emit(NotificationEvent{Level: NotificationInfo, Message: "second"})
		bus.Emit(SessionEndedEvent{})
	}()

	// Use a goroutine+timeout to verify Emit does not block.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked on full channel")
	}

	// Only the first event should have been enqueued.
	select {
	case evt := <-ch:
		if evt.Type() != "SessionStarted" {
			t.Errorf("expected SessionStarted, got %s", evt.Type())
		}
	default:
		t.Error("expected first event in channel")
	}
}

func TestEventBus_SubscribeAfterClose(t *testing.T) {
	bus := NewEventBus(10)
	bus.Close()

	ch := bus.Subscribe()
	if ch != nil {
		t.Error("expected nil channel from Subscribe after Close")
	}
}

func TestEventBus_ShutdownAlias(t *testing.T) {
	bus := NewEventBus(10)
	bus.Shutdown()

	ch := bus.Subscribe()
	if ch != nil {
		t.Error("expected nil channel from Subscribe after Shutdown")
	}
}
