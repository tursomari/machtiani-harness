package trajectory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultExcerptLen = 512
)

// Config controls writer behavior for a single unified trajectory stream.
//
// SessionID, Path, and Component must be provided.
type Config struct {
	SessionID  string
	Path       string
	Component  string
	ExcerptLen int
	// Flags for downstream instrumentation; stored for reference.
	StreamTokens bool
	VerboseLLM   bool
	OmitRepoRoot bool
}

// Writer appends structured trajectory events to a JSONL file. A writer is safe
// for concurrent use by multiple goroutines.
type Writer struct {
	cfg   Config
	file  *os.File
	mu    sync.Mutex
	seq   atomic.Int64
	once  sync.Once
	close func() error
}

// Event models the schema shared across components. Callers supply the dynamic
// fields while the writer fills in session/component/sequence/timestamp.
type Event struct {
	Level        string         `json:"level"`
	Kind         string         `json:"kind"`
	SpanID       string         `json:"span_id"`
	ParentSpanID string         `json:"parent_span_id,omitempty"`
	Payload      map[string]any `json:"payload"`
	Err          *ErrorInfo     `json:"err,omitempty"`
	Meta         map[string]any `json:"meta,omitempty"`
	Component    string         `json:"-"`
}

// ErrorInfo captures normalized error details.
type ErrorInfo struct {
	Message  string `json:"message,omitempty"`
	Category string `json:"category,omitempty"`
	Code     string `json:"code,omitempty"`
	Details  string `json:"details,omitempty"`
}

// Span represents an operation span. It records start and parent metadata so
// callers can compute durations easily when emitting end events.
type Span struct {
	ID        string
	ParentID  string
	StartedAt time.Time
}

// New creates a new trajectory writer configured for the given session. The
// returned writer must be closed when no longer needed.
func New(cfg Config) (*Writer, error) {
	cfg.SessionID = strings.TrimSpace(cfg.SessionID)
	cfg.Path = strings.TrimSpace(cfg.Path)
	cfg.Component = strings.TrimSpace(cfg.Component)
	if cfg.SessionID == "" {
		return nil, errors.New("trajectory: session id required")
	}
	if cfg.Path == "" {
		return nil, errors.New("trajectory: path required")
	}
	if cfg.Component == "" {
		return nil, errors.New("trajectory: component required")
	}
	if cfg.ExcerptLen <= 0 {
		cfg.ExcerptLen = defaultExcerptLen
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
		return nil, fmt.Errorf("trajectory: create directory: %w", err)
	}
	f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("trajectory: open file: %w", err)
	}
	w := &Writer{cfg: cfg, file: f}
	w.close = f.Close
	return w, nil
}

// Close flushes and releases the underlying file.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	var err error
	w.once.Do(func() {
		if w.close != nil {
			err = w.close()
		}
	})
	return err
}

// Emit writes an event to the JSONL stream. The writer augments the event with
// timestamp, session, component, and a per-writer sequence number.
func (w *Writer) Emit(ctx context.Context, evt Event) error {
	if w == nil {
		return nil
	}
	if evt.Level == "" {
		evt.Level = "info"
	}
	if evt.Kind == "" {
		return errors.New("trajectory: event kind required")
	}
	if evt.SpanID == "" {
		evt.SpanID = NewSpanID()
	}
	if evt.Payload == nil {
		evt.Payload = map[string]any{}
	}
	// Ensure payload includes version information placeholder if caller forgot;
	// this enforces schema stability during development phases.
	if _, ok := evt.Payload["event_version"]; !ok {
		evt.Payload["event_version"] = 1
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	seq := w.seq.Add(1)

	component := strings.TrimSpace(evt.Component)
	if component == "" {
		component = w.cfg.Component
	}

	record := map[string]any{
		"ts":         now,
		"session_id": w.cfg.SessionID,
		"component":  component,
		"level":      evt.Level,
		"kind":       evt.Kind,
		"span_id":    evt.SpanID,
		"seq":        seq,
		"payload":    evt.Payload,
	}
	if evt.ParentSpanID != "" {
		record["parent_span_id"] = evt.ParentSpanID
	}
	if evt.Err != nil {
		record["err"] = evt.Err
	}
	if evt.Meta != nil {
		record["meta"] = evt.Meta
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("trajectory: marshal event: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, err := w.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("trajectory: write event: %w", err)
	}
	return nil
}

// StartSpan allocates a new span identifier. Callers are responsible for
// emitting the corresponding start/end events.
func (w *Writer) StartSpan(parent string) Span {
	return Span{
		ID:        NewSpanID(),
		ParentID:  parent,
		StartedAt: time.Now().UTC(),
	}
}

// ExcerptLen returns the configured excerpt length for the writer, falling back
// to the default when unset.
func (w *Writer) ExcerptLen() int {
	if w == nil {
		return defaultExcerptLen
	}
	return w.cfg.ExcerptLen
}

// Config exposes the writer configuration for downstream helpers.
func (w *Writer) Config() Config {
	if w == nil {
		return Config{}
	}
	return w.cfg
}

// NewSpanID generates a random 128-bit identifier encoded as hex.
func NewSpanID() string {
	var buf [16]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		// rand.Reader should not fail; fallback to timestamp-based id in the
		// unlikely event of an error to keep the stream flowing.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

type ctxKey struct{}

// ContextWithWriter attaches a trajectory writer to the context.
func ContextWithWriter(ctx context.Context, w *Writer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKey{}, w)
}

// FromContext returns the writer stored in ctx, if any.
func FromContext(ctx context.Context) (*Writer, bool) {
	if ctx == nil {
		return nil, false
	}
	w, ok := ctx.Value(ctxKey{}).(*Writer)
	return w, ok && w != nil
}
