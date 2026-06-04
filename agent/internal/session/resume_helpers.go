package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/conversation"
)

// shellSessionIDForTurn returns continueID if non-empty, otherwise constructs
// a shell-agent session ID by formatting parentSessionID and step.
func shellSessionIDForTurn(continueID, parentSessionID string, step int) string {
	if strings.TrimSpace(continueID) != "" {
		return continueID
	}
	return fmt.Sprintf("%s/shell-agent/%d", parentSessionID, step)
}

// resumeShellAgentSessionID scans conv.Messages in reverse for a work_request
// message whose shell_agent_session_id metadata points to a state.json file
// that exists and contains the substring "Ongoing". Returns the session ID on
// the first match, or an empty string if none is found.
func resumeShellAgentSessionID(conv *conversation.Conversation) string {
	if conv == nil {
		return ""
	}
	for i := len(conv.Messages) - 1; i >= 0; i-- {
		msg := conv.Messages[i]
		if msg.Metadata == nil {
			continue
		}
		if msgType, ok := coerceString(msg.Metadata["type"]); !ok || strings.ToLower(msgType) != "work_request" {
			continue
		}
		sessionID, ok := coerceString(msg.Metadata["shell_agent_session_id"])
		if !ok || sessionID == "" {
			continue
		}
		stateFile := filepath.Join(".machtiani", "sessions", sessionID, "state.json")
		data, err := os.ReadFile(stateFile)
		if err != nil {
			return ""
		}
		if !strings.Contains(string(data), "Ongoing") {
			return ""
		}
		return sessionID
	}
	return ""
}

// coerceString extracts a non-empty trimmed string from a generic value.
func coerceString(val any) (string, bool) {
	switch v := val.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return "", false
		}
		return trimmed, true
	}
	return "", false
}
