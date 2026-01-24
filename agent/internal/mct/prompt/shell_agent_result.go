package prompt

import "strings"

const (
	shellAgentResultBlockBegin = "BEGIN_SHELL_AGENT_RESULT"
	shellAgentResultBlockEnd   = "END_SHELL_AGENT_RESULT"
)

// extractShellAgentResultBlock finds the final shell-agent result section from
// stdout text. It returns the lines between the BEGIN/END markers when present
// and non-empty.
func extractShellAgentResultBlock(output string) (string, bool) {
	start := strings.Index(output, shellAgentResultBlockBegin)
	if start == -1 {
		return "", false
	}
	start += len(shellAgentResultBlockBegin)

	end := strings.Index(output[start:], shellAgentResultBlockEnd)
	if end == -1 {
		return "", false
	}

	block := strings.TrimSpace(output[start : start+end])
	if block == "" {
		return "", false
	}

	return block, true
}
