package environments

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

const forgePrefix = "machtiani-forge"

// ForgeWrapper intercepts machtiani-forge commands to capture structured metadata
// and detect misuse.  Commands are passed through to the inner environment unchanged.
type ForgeWrapper struct {
	inner minisweagent.Environment
}

// NewForgeWrapper wraps an existing Environment with forge-command interception.
func NewForgeWrapper(inner minisweagent.Environment) *ForgeWrapper {
	return &ForgeWrapper{inner: inner}
}

// forgeMetadata carries structured JSON for forge command executions.
type forgeMetadata struct {
	Action      string `json:"action"`
	ExitCode    int    `json:"exit_code"`
	DurationMS  int64  `json:"duration_ms"`
	NoteBytes   int    `json:"note_bytes"`
	OutputBytes int    `json:"output_bytes"`
}

// Execute implements minisweagent.Environment.  Commands that begin with
// "machtiani-forge" are intercepted for metadata capture and misuse detection.
// All other commands pass through to the inner environment unchanged.
func (w *ForgeWrapper) Execute(ctx context.Context, command, cwd string) (minisweagent.ExecuteResult, error) {
	trimmed := strings.TrimSpace(command)
	if !strings.HasPrefix(trimmed, forgePrefix) {
		if regexp.MustCompile("(\\|\\s*|;\\s*|&&\\s*|\\|\\|\\s*|xargs\\s+)" + forgePrefix + "(\\s|$)").MatchString(trimmed) {
			return minisweagent.ExecuteResult{ReturnCode: 1, Output: "machtiani-forge error: machtiani-forge must be the first command and takes a single argument in single quotes. Do not pipe to machtiani-forge or use it with xargs. Usage: machtiani-forge your handoff note"}, nil
		}
		return w.inner.Execute(ctx, command, cwd)
	}

	// Extract the handoff note after "machtiani-forge".
	note := strings.TrimSpace(strings.TrimPrefix(trimmed, forgePrefix))

	start := time.Now()
	result, err := w.inner.Execute(context.WithoutCancel(ctx), command, cwd)
	elapsed := time.Since(start)

	// Build structured metadata.
	meta := forgeMetadata{
		Action:      "forge",
		ExitCode:    result.ReturnCode,
		DurationMS:  elapsed.Milliseconds(),
		NoteBytes:   len(note),
		OutputBytes: len(result.Output),
	}

	metaJSON, marshalErr := json.Marshal(meta)
	if marshalErr != nil {
		// Fallback: embed raw error in metadata.
		result.Metadata = fmt.Sprintf(`{"error":"marshal metadata: %s"}`, marshalErr.Error())
	} else {
		result.Metadata = string(metaJSON)
	}

	return result, err
}

// Start preserves the inner environment's observable command lifecycle. Forge
// commands retain their metadata and cancellation-resistant launch context;
// malformed forge pipelines fall back to Execute for the existing diagnostic.
func (w *ForgeWrapper) Start(ctx context.Context, command, cwd string) (minisweagent.RunningCommand, error) {
	trimmed := strings.TrimSpace(command)
	if !strings.HasPrefix(trimmed, forgePrefix) && regexp.MustCompile("(\\|\\s*|;\\s*|&&\\s*|\\|\\|\\s*|xargs\\s+)"+forgePrefix+"(\\s|$)").MatchString(trimmed) {
		return nil, minisweagent.ErrAsyncExecutionUnsupported
	}
	async, ok := w.inner.(minisweagent.AsyncEnvironment)
	if !ok {
		return nil, minisweagent.ErrAsyncExecutionUnsupported
	}
	if !strings.HasPrefix(trimmed, forgePrefix) {
		return async.Start(ctx, command, cwd)
	}
	running, err := async.Start(context.WithoutCancel(ctx), command, cwd)
	if err != nil {
		return nil, err
	}
	return &forgeRunningCommand{
		RunningCommand: running,
		noteBytes:      len(strings.TrimSpace(strings.TrimPrefix(trimmed, forgePrefix))),
		startedAt:      time.Now(),
	}, nil
}

type forgeRunningCommand struct {
	minisweagent.RunningCommand
	noteBytes int
	startedAt time.Time
	once      sync.Once
	result    minisweagent.ExecuteResult
	err       error
}

func (c *forgeRunningCommand) Wait() (minisweagent.ExecuteResult, error) {
	c.once.Do(func() {
		c.result, c.err = c.RunningCommand.Wait()
		meta := forgeMetadata{
			Action:      "forge",
			ExitCode:    c.result.ReturnCode,
			DurationMS:  time.Since(c.startedAt).Milliseconds(),
			NoteBytes:   c.noteBytes,
			OutputBytes: len(c.result.Output),
		}
		if payload, err := json.Marshal(meta); err == nil {
			c.result.Metadata = string(payload)
		} else {
			c.result.Metadata = fmt.Sprintf(`{"error":"marshal metadata: %s"}`, err.Error())
		}
	})
	return c.result, c.err
}

// Config delegates to the inner environment.
func (w *ForgeWrapper) Config() interface{} {
	return w.inner.Config()
}

// GetTemplateVars delegates to the inner environment.
func (w *ForgeWrapper) GetTemplateVars() map[string]interface{} {
	return w.inner.GetTemplateVars()
}

// GetSyncProgress delegates to the inner environment.
func (w *ForgeWrapper) GetSyncProgress() float64 {
	return w.inner.GetSyncProgress()
}

// GetSyncStatus delegates to the inner environment.
func (w *ForgeWrapper) GetSyncStatus() string {
	return w.inner.GetSyncStatus()
}
