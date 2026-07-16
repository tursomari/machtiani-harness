package environments

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

const forgePrefix = "mct-forge"

// ForgeWrapper intercepts mct-forge commands to capture structured metadata
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
// "mct-forge" are intercepted for metadata capture and misuse detection.
// All other commands pass through to the inner environment unchanged.
func (w *ForgeWrapper) Execute(ctx context.Context, command, cwd string) (minisweagent.ExecuteResult, error) {
	trimmed := strings.TrimSpace(command)
	if !strings.HasPrefix(trimmed, forgePrefix) {
		if regexp.MustCompile("(\\|\\s*|;\\s*|&&\\s*|\\|\\|\\s*|xargs\\s+)" + forgePrefix + "(\\s|$)").MatchString(trimmed) {
			return minisweagent.ExecuteResult{ReturnCode: 1, Output: "mct-forge error: mct-forge must be the first command and takes a single argument in single quotes. Do not pipe to mct-forge or use it with xargs. Usage: mct-forge your handoff note"}, nil
		}
		return w.inner.Execute(ctx, command, cwd)
	}

	// Extract the handoff note after "mct-forge".
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
