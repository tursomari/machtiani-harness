package trajectory

import (
	"crypto/sha256"
	"encoding/hex"
)

// TextExcerpt describes a redacted view of a larger string payload.
type TextExcerpt struct {
	First string `json:"text_excerpt_first,omitempty"`
	Last  string `json:"text_excerpt_last,omitempty"`
	Len   int    `json:"text_len"`
	SHA   string `json:"text_sha256"`
}

// MakeTextExcerpt produces an excerpt summary respecting the provided limit.
// When limit <= 0, no literal excerpts are included, only length and hash.
func MakeTextExcerpt(text string, limit int) TextExcerpt {
	bytes := []byte(text)
	total := len(bytes)
	sum := sha256.Sum256(bytes)

	out := TextExcerpt{
		Len: total,
		SHA: hex.EncodeToString(sum[:]),
	}
	if limit <= 0 || total == 0 {
		return out
	}

	runes := []rune(text)
	if limit >= len(runes) {
		out.First = text
		out.Last = text
		return out
	}
	out.First = string(runes[:limit])
	out.Last = string(runes[len(runes)-limit:])
	return out
}

// MergeExcerpt copies the excerpt fields into the provided payload map using
// the canonical key names. The map is created if nil.
func MergeExcerpt(payload map[string]any, excerpt TextExcerpt) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	if excerpt.First != "" {
		payload["text_excerpt_first"] = excerpt.First
	}
	if excerpt.Last != "" {
		payload["text_excerpt_last"] = excerpt.Last
	}
	payload["text_len"] = excerpt.Len
	payload["text_sha256"] = excerpt.SHA
	return payload
}

// MergeExcerptWithPrefix copies excerpt fields using a prefix (e.g., "prompt"
// or "response") so multiple excerpts can share the same payload map.
func MergeExcerptWithPrefix(payload map[string]any, excerpt TextExcerpt, prefix string) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	if prefix == "" {
		return MergeExcerpt(payload, excerpt)
	}
	if excerpt.First != "" {
		payload[prefix+"_excerpt_first"] = excerpt.First
	}
	if excerpt.Last != "" {
		payload[prefix+"_excerpt_last"] = excerpt.Last
	}
	payload[prefix+"_len"] = excerpt.Len
	payload[prefix+"_sha256"] = excerpt.SHA
	return payload
}
