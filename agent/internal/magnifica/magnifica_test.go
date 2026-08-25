package magnifica

import (
	"os"
	"strings"
	"testing"
)

func TestSelectQuoteForSessionGoldenVectors(t *testing.T) {
	file, err := os.Open("testdata/corpus-tiny.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	corpus, err := ParseCorpus(file)
	if err != nil {
		t.Fatalf("ParseCorpus: %v", err)
	}

	tests := []struct {
		sessionID string
		wantIndex int
		wantText  string
	}{
		// Indices were computed independently with Python's hashlib.sha256,
		// int.from_bytes(digest[:8], "big"), and modulo 4.
		{sessionID: "session-alpha", wantIndex: 3, wantText: "Delta bridge."},
		{sessionID: "golden-vector-1", wantIndex: 0, wantText: "Alpha horizon."},
		{sessionID: "golden-vector-2", wantIndex: 3, wantText: "Delta bridge."},
		{sessionID: " padded-session ", wantIndex: 2, wantText: "Gamma lantern."},
	}
	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			first, err := SelectQuoteForSession(tt.sessionID, corpus)
			if err != nil {
				t.Fatalf("SelectQuoteForSession: %v", err)
			}
			if first != corpus[tt.wantIndex] {
				t.Fatalf("selected quote = %#v, want corpus[%d] = %#v", first, tt.wantIndex, corpus[tt.wantIndex])
			}
			if first.Text != tt.wantText {
				t.Fatalf("selected text = %q, want %q", first.Text, tt.wantText)
			}

			second, err := SelectQuoteForSession(tt.sessionID, corpus)
			if err != nil {
				t.Fatalf("second SelectQuoteForSession: %v", err)
			}
			if second != first {
				t.Fatalf("repeated selection changed from %#v to %#v", first, second)
			}
		})
	}
}

func TestSelectQuoteForSessionMatchesEmbeddedCorpusVectors(t *testing.T) {
	corpus, err := LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	if got, want := len(corpus), 59; got != want {
		t.Fatalf("corpus length = %d, want %d", got, want)
	}
	last := corpus[len(corpus)-1]
	if last.Paragraph != 245 || last.Text != "Quo vadis, humanitas?" {
		t.Fatalf("final quote = %#v, want paragraph 245 and %q", last, "Quo vadis, humanitas?")
	}

	tests := []struct {
		sessionID string
		wantIndex int
		wantText  string
	}{
		// Indices and texts were recomputed from the refreshed corpus and
		// verified independently with Python.
		{sessionID: "session-alpha", wantIndex: 11, wantText: "In the abstract, technology in and of itself is not a solution to humanity’s problems, just as it is not inherently evil."},
		{sessionID: "golden-vector-1", wantIndex: 41, wantText: "A society is noble and decent, not least for its support of the pursuit of truth and its adherence to the most basic of truths."},
		{sessionID: "golden-vector-2", wantIndex: 6, wantText: "Technology should not be considered, in itself, as a force antagonistic to humanity."},
		{sessionID: " padded-session ", wantIndex: 19, wantText: "We can describe the common good as the social expression of the dignity recognized in every person."},
	}
	for _, tt := range tests {
		t.Run(tt.sessionID, func(t *testing.T) {
			got, err := SelectQuoteForSession(tt.sessionID, corpus)
			if err != nil {
				t.Fatalf("SelectQuoteForSession: %v", err)
			}
			if got != corpus[tt.wantIndex] {
				t.Fatalf("selected quote = %#v, want corpus[%d] = %#v", got, tt.wantIndex, corpus[tt.wantIndex])
			}
			if got.Text != tt.wantText {
				t.Fatalf("selected text = %q, want %q", got.Text, tt.wantText)
			}
		})
	}
}

func TestSelectQuoteForSessionRejectsEmptyCorpus(t *testing.T) {
	for _, corpus := range [][]Quote{nil, {}} {
		if _, err := SelectQuoteForSession("x", corpus); err == nil {
			t.Errorf("SelectQuoteForSession(%#v) error = nil, want error", corpus)
		}
	}
}

func TestSelectQuoteForSessionRejectsMalformedEntries(t *testing.T) {
	valid := Quote{Paragraph: 1, Line: 1, Text: "Valid quote."}
	tests := []struct {
		name  string
		quote Quote
	}{
		{name: "paragraph", quote: Quote{Paragraph: 0, Line: 1, Text: "Invalid paragraph."}},
		{name: "line", quote: Quote{Paragraph: 1, Line: 0, Text: "Invalid line."}},
		{name: "empty text", quote: Quote{Paragraph: 1, Line: 1, Text: ""}},
		{name: "whitespace text", quote: Quote{Paragraph: 1, Line: 1, Text: " \t "}},
		{name: "carriage return", quote: Quote{Paragraph: 1, Line: 1, Text: "line1\rline2"}},
		{name: "newline", quote: Quote{Paragraph: 1, Line: 1, Text: "line1\nline2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := SelectQuoteForSession("x", []Quote{valid, tt.quote}); err == nil {
				t.Fatalf("SelectQuoteForSession accepted malformed quote %#v", tt.quote)
			}
		})
	}
}

func TestParseCorpusValidJSONL(t *testing.T) {
	input := "\n  {\"paragraph\":1,\"line\":2,\"quote\":\"  First quote.  \"}\n\t\n{\"paragraph\":3,\"line\":4,\"quote\":\"Second quote.\"}\n"
	want := []Quote{
		{Paragraph: 1, Line: 2, Text: "First quote."},
		{Paragraph: 3, Line: 4, Text: "Second quote."},
	}

	got, err := ParseCorpus(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseCorpus: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("ParseCorpus returned %d quotes, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("quote %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestParseCorpusRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "blank", input: " \n\t\n"},
		{name: "malformed JSON", input: "{not json}\n"},
		{name: "paragraph", input: "{\"paragraph\":0,\"line\":1,\"quote\":\"text\"}\n"},
		{name: "line", input: "{\"paragraph\":1,\"line\":0,\"quote\":\"text\"}\n"},
		{name: "empty text", input: "{\"paragraph\":1,\"line\":1,\"quote\":\"  \"}\n"},
		{name: "carriage return", input: "{\"paragraph\":1,\"line\":1,\"quote\":\"line1\\rline2\"}\n"},
		{name: "newline", input: "{\"paragraph\":1,\"line\":1,\"quote\":\"line1\\nline2\"}\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCorpus(strings.NewReader(tt.input)); err == nil {
				t.Fatalf("ParseCorpus(%q) error = nil, want error", tt.input)
			}
		})
	}
}
