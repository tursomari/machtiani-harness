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

// extractShellAgentResultText extracts the Result payload from a result block.
// It returns false when no Result line is present.
func extractShellAgentResultText(block string) (string, bool) {
	lines := strings.Split(block, "\n")
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if !strings.HasPrefix(lower, "result:") {
			continue
		}
		payload := strings.TrimSpace(trimmed[len("result:"):])
		rest := strings.Join(lines[idx+1:], "\n")
		if rest != "" {
			if payload != "" {
				return payload + "\n" + rest, true
			}
			return rest, true
		}
		return payload, true
	}
	return "", false
}
