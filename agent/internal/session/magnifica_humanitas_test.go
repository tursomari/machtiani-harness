package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/magnifica"
)

func TestSessionStateProjectsMagnificaHumanitas(t *testing.T) {
	conv := conversation.New("magnifica-projection", "Project the quote")
	conv.Goal = "Project the quote"
	conv.MagnificaHumanitas = &conversation.MagnificaHumanitas{
		Paragraph: 12,
		Line:      2,
		Quote:     "A projected quotation.",
	}

	state, err := sessionStateFromConversation(conv, conv.SessionID)
	if err != nil {
		t.Fatalf("sessionStateFromConversation: %v", err)
	}
	if state.MagnificaHumanitas == nil {
		t.Fatal("projected MagnificaHumanitas is nil")
	}
	if *state.MagnificaHumanitas != *conv.MagnificaHumanitas {
		t.Fatalf("projected MagnificaHumanitas = %#v, want %#v", state.MagnificaHumanitas, conv.MagnificaHumanitas)
	}
	if state.MagnificaHumanitas == conv.MagnificaHumanitas {
		t.Fatal("projection retained the conversation pointer instead of cloning it")
	}
}

func TestSessionStateOmitsAbsentMagnificaHumanitas(t *testing.T) {
	conv := conversation.New("magnifica-projection-nil", "No quote")
	conv.Goal = "No quote"

	state, err := sessionStateFromConversation(conv, conv.SessionID)
	if err != nil {
		t.Fatalf("sessionStateFromConversation: %v", err)
	}
	if state.MagnificaHumanitas != nil {
		t.Fatalf("projected MagnificaHumanitas = %#v, want nil", state.MagnificaHumanitas)
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode SessionState JSON: %v", err)
	}
	if _, ok := object["magnifica_humanitas"]; ok {
		t.Fatalf("nil magnifica_humanitas was serialized: %s", data)
	}
}

func TestNewLegacyConfigCopiesMagnificaHumanitas(t *testing.T) {
	cfg := newLegacyConfig(Config{MagnificaHumanitas: true})
	if !cfg.magnificaHumanitas {
		t.Fatal("newLegacyConfig did not copy MagnificaHumanitas")
	}
}

func TestConversationRecorderFreshDisabledLeavesMagnificaHumanitasNil(t *testing.T) {
	recorder := newConversationRecorder(nil, "magnifica-fresh-disabled", "Goal", "", false, nil, false, false)
	recorder.quoteSource = func() ([]magnifica.Quote, error) {
		return magnificaTestCorpus(), nil
	}

	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if recorder.Conversation().MagnificaHumanitas != nil {
		t.Fatalf("MagnificaHumanitas = %#v, want nil", recorder.Conversation().MagnificaHumanitas)
	}
}

func TestConversationRecorderFreshEnabledSelectsDeterministicQuote(t *testing.T) {
	const sessionID = "magnifica-fresh-enabled"
	corpus := magnificaTestCorpus()
	want, err := magnifica.SelectQuoteForSession(sessionID, corpus)
	if err != nil {
		t.Fatalf("SelectQuoteForSession: %v", err)
	}
	recorder := newConversationRecorder(nil, sessionID, "Goal", "", false, nil, false, true)
	recorder.quoteSource = func() ([]magnifica.Quote, error) { return corpus, nil }

	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertConversationQuote(t, recorder.Conversation(), want)
}

func TestConversationRecorderResumeKeepsStoredMagnificaHumanitas(t *testing.T) {
	const sessionID = "magnifica-resume-stored"
	stored := &conversation.MagnificaHumanitas{
		Paragraph: 99,
		Line:      7,
		Quote:     "The stored selection remains authoritative.",
	}
	path := filepath.Join(t.TempDir(), "conversation.json")
	conv := conversation.New(sessionID, "Goal")
	conv.MagnificaHumanitas = stored.Clone()
	writeRecorderConversation(t, path, conv)

	recorder := newConversationRecorder(nil, sessionID, "Goal", path, true, nil, false, true)
	recorder.quoteSource = func() ([]magnifica.Quote, error) {
		return []magnifica.Quote{{Paragraph: 1, Line: 1, Text: "A changed corpus quote."}}, nil
	}
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if recorder.Conversation().MagnificaHumanitas == nil || *recorder.Conversation().MagnificaHumanitas != *stored {
		t.Fatalf("stored quote changed to %#v, want %#v", recorder.Conversation().MagnificaHumanitas, stored)
	}
}

func TestConversationRecorderResumeEnabledBackfillsMissingMagnificaHumanitas(t *testing.T) {
	const sessionID = "magnifica-resume-missing"
	path := filepath.Join(t.TempDir(), "conversation.json")
	writeRecorderConversation(t, path, conversation.New(sessionID, "Goal"))
	corpus := magnificaTestCorpus()
	want, err := magnifica.SelectQuoteForSession(sessionID, corpus)
	if err != nil {
		t.Fatalf("SelectQuoteForSession: %v", err)
	}

	recorder := newConversationRecorder(nil, sessionID, "Goal", path, true, nil, false, true)
	recorder.quoteSource = func() ([]magnifica.Quote, error) { return corpus, nil }
	if err := recorder.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	assertConversationQuote(t, recorder.Conversation(), want)
}

func TestConversationRecorderEnabledReturnsQuoteSourceError(t *testing.T) {
	wantErr := errors.New("fixture corpus unavailable")
	recorder := newConversationRecorder(nil, "magnifica-source-error", "Goal", "", false, nil, false, true)
	recorder.quoteSource = func() ([]magnifica.Quote, error) { return nil, wantErr }

	err := recorder.Load()
	if !errors.Is(err, wantErr) {
		t.Fatalf("Load error = %v, want wrapped %v", err, wantErr)
	}
}

func magnificaTestCorpus() []magnifica.Quote {
	return []magnifica.Quote{
		{Paragraph: 1, Line: 1, Text: "First fixture quote."},
		{Paragraph: 2, Line: 3, Text: "Second fixture quote."},
		{Paragraph: 5, Line: 8, Text: "Third fixture quote."},
	}
}

func assertConversationQuote(t *testing.T, conv *conversation.Conversation, want magnifica.Quote) {
	t.Helper()
	if conv.MagnificaHumanitas == nil {
		t.Fatal("MagnificaHumanitas is nil")
	}
	if got := *conv.MagnificaHumanitas; got.Paragraph != want.Paragraph || got.Line != want.Line || got.Quote != want.Text {
		t.Fatalf("MagnificaHumanitas = %#v, want paragraph=%d line=%d quote=%q", got, want.Paragraph, want.Line, want.Text)
	}
}

func writeRecorderConversation(t *testing.T, path string, conv *conversation.Conversation) {
	t.Helper()
	data, err := conv.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write conversation: %v", err)
	}
}
