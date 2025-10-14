package naming

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/tursomari/machtiani/agent/internal/mct/llm"
)

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "and": true, "or": true, "to": true,
	"of": true, "in": true, "for": true, "with": true, "on": true, "at": true,
	"from": true, "by": true, "about": true, "as": true, "into": true, "is": true,
	"are": true, "was": true, "were": true, "be": true, "this": true, "that": true,
	"it": true, "my": true, "our": true, "your": true, "we": true, "you": true,
}

var nonASCII = regexp.MustCompile(`[^\x00-\x7F]+`)
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Generate attempts an LLM-based filename, falling back to deterministic slug.
func Generate(ctx context.Context, prompt string, model llm.ResolvedModel) string {
	if strings.TrimSpace(model.APIKey) != "" && strings.TrimSpace(model.BaseURL) != "" && strings.TrimSpace(model.Model) != "" {
		sys := "You are a naming assistant. Return only a short, kebab-case title (<= 60 chars), no extension, no quotes."
		user := "Suggest a concise filename for this prompt:\n\n" + prompt
		msg := []llm.Message{{Role: "system", Content: sys}, {Role: "user", Content: user}}
		ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		if out, err := llm.ChatWithResolved(ctx, model, nil, msg); err == nil {
			name := sanitize(out)
			if name != "" {
				return name
			}
		}
	}
	return slug(prompt)
}

func sanitize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"' ")
	s = strings.SplitN(s, "\n", 2)[0]
	s = nonASCII.ReplaceAllString(s, "")
	s = strings.ToLower(s)
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
		s = strings.TrimRight(s, "-")
	}
	if s == "" {
		return "machtiani-response"
	}
	return s
}

func slug(s string) string {
	// Take first 12 significant words
	words := splitWordsASCII(s)
	keep := make([]string, 0, len(words))
	for _, w := range words {
		lw := strings.ToLower(w)
		if stopwords[lw] {
			continue
		}
		keep = append(keep, lw)
		if len(keep) >= 12 {
			break
		}
	}
	if len(keep) == 0 {
		return "machtiani-response"
	}
	joined := strings.Join(keep, "-")
	joined = nonAlnum.ReplaceAllString(joined, "-")
	joined = strings.Trim(joined, "-")
	if len(joined) > 60 {
		joined = joined[:60]
		joined = strings.TrimRight(joined, "-")
	}
	if joined == "" {
		return "machtiani-response"
	}
	return joined
}

func splitWordsASCII(s string) []string {
	// Normalize to ASCII and split on non-letter/digit
	// Remove punctuation
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r > 127 {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	fields := strings.Fields(b.String())
	// Deduplicate consecutive duplicates, keep order
	out := make([]string, 0, len(fields))
	var prev string
	for _, f := range fields {
		if f == prev {
			continue
		}
		out = append(out, f)
		prev = f
	}
	return out
}
