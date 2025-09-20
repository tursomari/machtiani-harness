package planner

import (
    "context"
    "os"
    "strings"
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

func TestParseDecisionPatch(t *testing.T) {
    body := "Decision: patch\n{\n  \"edits\": []\n}"
    d, payload := parseDecision(body)
    if d != DecisionPatch || strings.TrimSpace(payload) == "" {
        t.Fatalf("expected DecisionPatch with non-empty payload, got %v %q", d, payload)
    }
}

func TestChatMissingModel(t *testing.T) {
    restore := unsetEnv(t, []string{
        "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL",
        "AGENT_MODEL_API_KEY", "AGENT_MODEL_BASE_URL", "AGENT_MODEL",
        "MCT_MODEL_API_KEY", "MCT_MODEL_BASE_URL", "MCT_MODEL",
    })
    defer restore()
    c := NewClient(ClientConfig{AgentModelAPIKey: "k", AgentModelBaseURL: "http://example"})
    _, err := c.chat(context.Background(), "", "hi")
    if err == nil || !containsErr(err.Error(), "missing model config") {
        t.Fatalf("expected missing model error, got %v", err)
    }
}

func TestChatMissingBaseURL(t *testing.T) {
    restore := unsetEnv(t, []string{
        "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL",
        "AGENT_MODEL_API_KEY", "AGENT_MODEL_BASE_URL", "AGENT_MODEL",
        "MCT_MODEL_API_KEY", "MCT_MODEL_BASE_URL", "MCT_MODEL",
    })
    defer restore()
    c := NewClient(ClientConfig{AgentModel: "m", AgentModelAPIKey: "k"})
    _, err := c.chat(context.Background(), "", "hi")
    if err == nil || !containsErr(err.Error(), "missing base URL") {
        t.Fatalf("expected missing base URL error, got %v", err)
    }
}

func TestChatMissingAPIKey(t *testing.T) {
    restore := unsetEnv(t, []string{
        "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL",
        "AGENT_MODEL_API_KEY", "AGENT_MODEL_BASE_URL", "AGENT_MODEL",
        "MCT_MODEL_API_KEY", "MCT_MODEL_BASE_URL", "MCT_MODEL",
    })
    defer restore()
    c := NewClient(ClientConfig{AgentModel: "m", AgentModelBaseURL: "http://example"})
    _, err := c.chat(context.Background(), "", "hi")
    if err == nil || !containsErr(err.Error(), "missing API key") {
        t.Fatalf("expected missing API key error, got %v", err)
    }
}

func containsErr(s, sub string) bool { return index(s, sub) >= 0 }

func unsetEnv(t *testing.T, keys []string) func() {
    t.Helper()
    orig := map[string]*string{}
    for _, k := range keys {
        if v, ok := os.LookupEnv(k); ok {
            vv := v
            orig[k] = &vv
        } else {
            orig[k] = nil
        }
        _ = os.Unsetenv(k)
    }
    return func() {
        for k, pv := range orig {
            if pv == nil {
                _ = os.Unsetenv(k)
            } else {
                _ = os.Setenv(k, *pv)
            }
        }
    }
}

func TestFinalizePromptContainsTranscript(t *testing.T) {
    c := NewClient(ClientConfig{DryRun: true})
    goal := "Goal text here"
    transcript := "# mct-agent Transcript\n\nGoal:\nGoal text here\n\n## Turn 1\nQuestion:\nQ1\n\nRetrieved File Paths:\n- a.go\n- b.md\n\nAnswer:\nans\n"
    p := c.finalizePrompt(goal, transcript)
    if !containsAll(p, []string{"Goal text here", "Transcript", "Q1", "a.go", "ans"}) {
        t.Fatalf("finalize prompt missing expected content: %s", p)
    }
}

func TestPlanPromptIncludesPatch(t *testing.T) {
    c := NewClient(ClientConfig{DryRun: true})
    p := c.planPrompt("g", "t", 2, 4)
    if !contains(p, "Decision: ask|patch|finalize") {
        t.Fatalf("plan prompt missing patch option: %s", p)
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
