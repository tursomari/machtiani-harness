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

func TestParseDecisionInstruction(t *testing.T) {
    s := "Decision: ask\nInstruction: List key init functions in startup path."
    d, q := parseDecision(s)
    if d != DecisionAsk || q == "" {
        t.Fatalf("unexpected parse for Instruction: %v %q", d, q)
    }
}

func TestParseDecisionMessage(t *testing.T) {
    s := "Decision: ask\nMessage: Identify the main entrypoint file and its imports."
    d, q := parseDecision(s)
    if d != DecisionAsk || q == "" {
        t.Fatalf("unexpected parse for Message: %v %q", d, q)
    }
}

func TestFinalizePromptContainsTranscript(t *testing.T) {
    c := NewClient(ClientConfig{DryRun: true})
    transcript := "# mct-agent Transcript\n\nGoal:\nGoal text here\n\n## Turn 1\nQuestion:\nQ1\n\nRetrieved File Paths:\n- a.go\n- b.md\n\nAnswer:\nans\n"
    p := c.finalizePrompt(transcript)
    if !containsAll(p, []string{"Transcript", "Q1", "a.go", "ans"}) {
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
