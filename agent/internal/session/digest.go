package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
)

// CanonicalConversationDigest returns the SHA-256 digest of the conversation's
// semantic content. The identity and lifecycle fields session_id, created_at,
// updated_at, forked_from, forked_hash, and archived are normalized before
// encoding and therefore do not affect the digest. Every other Conversation
// field is encoded with encoding/json, including messages, prompts, goals,
// status, turn counts, model selection, and runner state.
func CanonicalConversationDigest(conv *conversation.Conversation) (string, error) {
	if conv == nil {
		return "", errors.New("conversation is nil")
	}

	canonical := *conv
	canonical.SessionID = ""
	canonical.Archived = false
	canonical.ForkedFrom = ""
	canonical.ForkedHash = ""
	canonical.CreatedAt = time.Time{}
	canonical.UpdatedAt = time.Time{}

	data, err := json.Marshal(&canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
