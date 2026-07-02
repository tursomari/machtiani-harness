package ui

import "sync"

// EventBus manages a typed event channel system for distributing
// DisplayEvent values to registered subscribers.
type EventBus struct {
	mu          sync.Mutex
	subscribers map[<-chan DisplayEvent]chan DisplayEvent
	bufferSize  int
	closed      bool
}

// NewEventBus creates a new EventBus with the given buffer size used for
// each subscriber's channel. If bufferSize <= 0, it defaults to 256.
func NewEventBus(bufferSize int) *EventBus {
	if bufferSize <= 0 {
		bufferSize = 256
	}
	return &EventBus{
		subscribers: make(map[<-chan DisplayEvent]chan DisplayEvent),
		bufferSize:  bufferSize,
	}
}

// Emit sends an event to all subscribers in a non-blocking manner. If a
// subscriber's channel is full, the event is dropped for that subscriber.
// The subscribers map is snapshotted under the mutex and iterated outside
// the lock to prevent deadlocks if a subscriber is slow.
func (b *EventBus) Emit(event DisplayEvent) {
	b.mu.Lock()
	snapshot := make([]chan DisplayEvent, 0, len(b.subscribers))
	for _, ch := range b.subscribers {
		snapshot = append(snapshot, ch)
	}
	b.mu.Unlock()

	for _, ch := range snapshot {
		select {
		case ch <- event:
		default:
			// Drop event when the subscriber channel is full (non-blocking).
		}
	}
}

// Subscribe creates a new buffered channel of DisplayEvent with capacity
// bufferSize, registers it in the subscribers map, and returns a
// receive-only channel. Each subscriber receives its own independent
// channel. If the EventBus has been closed, Subscribe returns nil.
func (b *EventBus) Subscribe() <-chan DisplayEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		return nil
	}

	ch := make(chan DisplayEvent, b.bufferSize)
	b.subscribers[ch] = ch
	return ch
}

// Unsubscribe removes the given subscriber channel from the EventBus. The
// channel is not closed by this call; the caller retains ownership.
func (b *EventBus) Unsubscribe(ch <-chan DisplayEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
}

// Close closes all subscriber channels, clears the subscribers map, and
// marks the EventBus as closed. After Close, no new events are delivered
// and new subscribers are rejected (Subscribe returns nil).
func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true
	for _, ch := range b.subscribers {
		close(ch)
	}
	b.subscribers = make(map[<-chan DisplayEvent]chan DisplayEvent)
}

// Shutdown is an alias for Close, provided for lifecycle semantics.
func (b *EventBus) Shutdown() {
	b.Close()
}
