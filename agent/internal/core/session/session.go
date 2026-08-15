// Package session derives conversation history for the MCT prompt layer from
// the authoritative per-session conversation.json on disk. The legacy
// ~/.machtiani/sessions/session-<id>.json file has been retired: history is no
// longer persisted by this package, and discovery-state persistence (which was
// dead code) has been removed. FileMeta is retained because the file-discovery
// runtime still uses it as an in-memory value type.
package session

import (
	"fmt"
	"os"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	"github.com/tursomari/machtiani/agent/internal/core/contextbuilder"
)

// FileMeta captures the metadata recorded for a file mirrored into the
// discovery workspace. The hash currently reflects the SHA256 digest of the
// file contents, allowing change detection between sync events. It is used as
// an in-memory value type by the file-discovery runtime and is no longer
// persisted by this package.
type FileMeta struct {
	Hash    string `json:"hash,omitempty"`
	Size    int64  `json:"size,omitempty"`
	ModTime int64  `json:"mod_time,omitempty"`
	Tracked bool   `json:"tracked,omitempty"`
}

// LoadHistory returns the prompt-layer conversation history for the current
// session. When MACHTIANI_SESSION_ID is set, the history is derived from the
// session's conversation.json. Otherwise an empty slice is returned — the MCT
// prompt layer no longer maintains a parallel on-disk history.
func LoadHistory() ([]contextbuilder.Message, error) {
	sessionID := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_ID"))
	if sessionID == "" {
		return []contextbuilder.Message{}, nil
	}
	path, err := artifacts.SessionConversationFile(sessionID)
	if err != nil {
		return []contextbuilder.Message{}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []contextbuilder.Message{}, nil
		}
		return nil, fmt.Errorf("read conversation file: %w", err)
	}
	conv, err := conversation.Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("parse conversation file: %w", err)
	}
	return historyFromConversation(conv), nil
}

// historyFromConversation distills a conversation into the work_request /
// work_result pair history consumed by the shell agent. Per the spec, the
// shell agent receives turn summaries — work_request / work_result pairs —
// rather than the full transcript or plain user/assistant messages (those
// already flow through the planner via ToChatMessages). There is
// intentionally no turn cap: the shell agent gets the complete history so it
// can reason about the full session, not a truncated window.
func historyFromConversation(conv *conversation.Conversation) []contextbuilder.Message {
	if conv == nil {
		return []contextbuilder.Message{}
	}
	out := make([]contextbuilder.Message, 0, len(conv.Messages))
	for _, msg := range conv.Messages {
		mtype := messageType(msg.Metadata)
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		switch mtype {
		case "work_request":
			out = append(out, contextbuilder.Message{Role: "assistant", Content: fmt.Sprintf("[work_request] %s", msg.Content)})
		case "work_result":
			out = append(out, contextbuilder.Message{Role: "assistant", Content: fmt.Sprintf("[work_result] %s", msg.Content)})
		}
	}
	return out
}

func messageType(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	raw, ok := meta["type"]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return ""
	}
}
