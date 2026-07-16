package utils

import (
	"io"
	"strings"
)

// LogFilterWriter wraps an io.Writer and filters out specific log messages
// before they reach the underlying writer.
type LogFilterWriter struct {
	underlying io.Writer
	exclusions []string
}

// NewLogFilterWriter creates a log writer that filters messages matching
// any of the exclusion patterns. Filtered lines are silently dropped;
// all other output is passed through unchanged to the underlying writer.
func NewLogFilterWriter(underlying io.Writer, exclusions ...string) *LogFilterWriter {
	return &LogFilterWriter{
		underlying: underlying,
		exclusions: exclusions,
	}
}

// Write implements io.Writer, filtering out lines matching exclusion patterns.
// Lines containing any of the exclusion patterns are dropped from output.
// For lines that pass the filter, the full byte count is reported as written.
// For filtered lines, the full input byte count is still reported to maintain
// correct semantics with the io.Writer interface.
func (lfw *LogFilterWriter) Write(p []byte) (int, error) {
	line := string(p)

	// Check if line matches any exclusion pattern
	for _, pattern := range lfw.exclusions {
		if strings.Contains(line, pattern) {
			// Drop this line but report full byte count written to maintain
			// io.Writer contract (write succeeded, no error)
			return len(p), nil
		}
	}

	// Line passes filter, write to underlying writer
	return lfw.underlying.Write(p)
}
