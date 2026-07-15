package sessionfiles

import (
	"path/filepath"
	"strconv"
	"strings"
)

// DisposableKind identifies session files that are never required for resume
// or normal operation and may be omitted from copies or explicitly pruned.
type DisposableKind string

const (
	DisposableNone            DisposableKind = ""
	DisposableLLMInputLog     DisposableKind = "llm_input_log"
	DisposableShellAgentState DisposableKind = "shell_agent_state"
)

// Classify returns the disposable kind for a path relative to one session
// directory. Unknown paths are always retained.
func Classify(relativePath string) DisposableKind {
	cleaned := filepath.ToSlash(filepath.Clean(strings.TrimSpace(relativePath)))
	if cleaned == "artifacts/llm/inputs.jsonl" {
		return DisposableLLMInputLog
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) != 3 || parts[0] != "shell-agent" || parts[2] != "state.json" {
		return DisposableNone
	}
	turn, err := strconv.Atoi(parts[1])
	if err != nil || turn < 0 {
		return DisposableNone
	}
	return DisposableShellAgentState
}
