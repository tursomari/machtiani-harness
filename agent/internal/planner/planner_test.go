package planner

import (
    "testing"
)

func TestParseDecision(t *testing.T) {
    s := "Decision: ask\nQuestion: What is the main init path?"
    d, q := parseDecision(s)
    if d != DecisionAsk || q == "" {
        t.Fatalf("unexpected parse: %v %q", d, q)
    }
}

func TestFinalizePromptContainsEvidence(t *testing.T) {
    c := NewClient(ClientConfig{DryRun: true})
    ev := []Turn{{Step:1, Question:"Q1", RetrievedFilePaths:[]string{"a.go","b.md"}, AnswerExcerpt:"ans"}}
    p := c.finalizePrompt("Goal", ev, "sum")
    if !containsAll(p, []string{"Goal", "Q1", "a.go", "ans"}) {
        t.Fatalf("finalize prompt missing expected content: %s", p)
    }
}

func containsAll(s string, subs []string) bool {
    for _, sub := range subs {
        if !contains(s, sub) { return false }
    }
    return true
}

func contains(s, sub string) bool { return len(s) >= len(sub) && ( (len(sub)==0) || (index(s, sub) >= 0) ) }

func index(s, sub string) int {
    // simple substring search to avoid importing strings for a micro test
    for i := 0; i+len(sub) <= len(s); i++ {
        if s[i:i+len(sub)] == sub { return i }
    }
    return -1
}

