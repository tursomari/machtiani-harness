// Package magnifica provides deterministic selection from the Magnifica
// Humanitas quote corpus.
package magnifica

import (
	"bufio"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Quote is a single entry in the Magnifica Humanitas corpus.
type Quote struct {
	Paragraph int    `json:"paragraph"`
	Line      int    `json:"line"`
	Text      string `json:"quote"`
}

//go:embed assets/magnifica_humanitas.jsonl
var embeddedCorpus string

// LoadEmbedded parses and validates the embedded Magnifica Humanitas corpus.
func LoadEmbedded() ([]Quote, error) {
	return ParseCorpus(strings.NewReader(embeddedCorpus))
}

// ParseCorpus parses and validates a JSON Lines quote corpus. Blank lines are
// ignored, and surrounding whitespace is removed from quote text.
func ParseCorpus(reader io.Reader) ([]Quote, error) {
	if reader == nil {
		return nil, fmt.Errorf("parse quote corpus: nil reader")
	}

	var quotes []Quote
	scanner := bufio.NewScanner(reader)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}

		var quote Quote
		if err := json.Unmarshal(scanner.Bytes(), &quote); err != nil {
			return nil, fmt.Errorf("parse quote corpus line %d: %w", lineNumber, err)
		}
		if err := validateQuote(quote); err != nil {
			return nil, fmt.Errorf("parse quote corpus line %d: %w", lineNumber, err)
		}
		quote.Text = strings.TrimSpace(quote.Text)
		quotes = append(quotes, quote)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan quote corpus: %w", err)
	}
	if len(quotes) == 0 {
		return nil, fmt.Errorf("quote corpus is empty")
	}
	return quotes, nil
}

// SelectQuoteForSession validates corpus and selects a quote deterministically
// from the SHA-256 digest of the whitespace-trimmed session ID.
func SelectQuoteForSession(sessionID string, corpus []Quote) (Quote, error) {
	if len(corpus) == 0 {
		return Quote{}, fmt.Errorf("quote corpus is empty")
	}
	for index, quote := range corpus {
		if err := validateQuote(quote); err != nil {
			return Quote{}, fmt.Errorf("invalid quote at index %d: %w", index, err)
		}
	}

	digest := sha256.Sum256([]byte(strings.TrimSpace(sessionID)))
	index := binary.BigEndian.Uint64(digest[:8]) % uint64(len(corpus))
	return corpus[index], nil
}

func validateQuote(quote Quote) error {
	if quote.Paragraph <= 0 {
		return fmt.Errorf("paragraph must be positive")
	}
	if quote.Line <= 0 {
		return fmt.Errorf("line must be positive")
	}
	if strings.ContainsAny(quote.Text, "\r\n") {
		return fmt.Errorf("quote text contains a line break")
	}
	if strings.TrimSpace(quote.Text) == "" {
		return fmt.Errorf("quote text is empty")
	}
	return nil
}
