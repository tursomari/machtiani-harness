package session

import (
	"testing"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/magnifica"
)

func TestForkSessionWithMagnificaHumanitasReselectsForDestination(t *testing.T) {
	setupForkTestWorkingDirectory(t)
	const (
		sourceID      = "magnifica-fork-enabled-source"
		destinationID = "magnifica-fork-enabled-destination"
	)
	source := conversation.New(sourceID, "Fork with a new quote")
	source.MagnificaHumanitas = &conversation.MagnificaHumanitas{
		Paragraph: 999,
		Line:      9,
		Quote:     "This parent-only quote must not be copied.",
	}
	writeForkTestConversation(t, source)

	newID, err := ForkSessionWithOptions(sourceID, destinationID, ForkOptions{MagnificaHumanitas: true})
	if err != nil {
		t.Fatalf("ForkSessionWithOptions: %v", err)
	}
	corpus, err := magnifica.LoadEmbedded()
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	want, err := magnifica.SelectQuoteForSession(newID, corpus)
	if err != nil {
		t.Fatalf("SelectQuoteForSession: %v", err)
	}
	assertConversationQuote(t, readForkTestConversation(t, newID), want)
}

func TestForkSessionDisabledClearsParentMagnificaHumanitas(t *testing.T) {
	setupForkTestWorkingDirectory(t)
	const (
		sourceID      = "magnifica-fork-disabled-source"
		destinationID = "magnifica-fork-disabled-destination"
	)
	source := conversation.New(sourceID, "Fork without a quote")
	source.MagnificaHumanitas = &conversation.MagnificaHumanitas{
		Paragraph: 88,
		Line:      4,
		Quote:     "This stale parent quote must be cleared.",
	}
	writeForkTestConversation(t, source)

	newID, err := ForkSession(sourceID, destinationID)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if got := readForkTestConversation(t, newID).MagnificaHumanitas; got != nil {
		t.Fatalf("forked MagnificaHumanitas = %#v, want nil", got)
	}
}
