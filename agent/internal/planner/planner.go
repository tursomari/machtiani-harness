package planner

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "net/http"
    "os"
    "strings"
)

type Decision string

const (
    DecisionAsk      Decision = "ask"
    DecisionFinalize Decision = "finalize"
)

type ClientConfig struct {
    AgentModel        string
    AgentModelAPIKey  string
    AgentModelBaseURL string
    Verbose           bool
    DryRun            bool
}

type Client struct {
    cfg        ClientConfig
    httpClient *http.Client
}

var warnedLegacyEnv bool

func NewClient(cfg ClientConfig) *Client {
    c := &Client{cfg: cfg, httpClient: &http.Client{}}
    // Resolve from OPENAI_* first, then legacy envs with a one-time warning
    // Model
    if strings.TrimSpace(c.cfg.AgentModel) == "" {
        c.cfg.AgentModel = os.Getenv("OPENAI_MODEL")
        if strings.TrimSpace(c.cfg.AgentModel) == "" {
            // legacy fallbacks
            c.cfg.AgentModel = firstNonEmptyEnv("AGENT_MODEL", "MCT_MODEL")
            if c.cfg.AgentModel != "" { warnLegacyOnce() }
        }
    }
    // API key
    if strings.TrimSpace(c.cfg.AgentModelAPIKey) == "" {
        c.cfg.AgentModelAPIKey = os.Getenv("OPENAI_API_KEY")
        if strings.TrimSpace(c.cfg.AgentModelAPIKey) == "" {
            c.cfg.AgentModelAPIKey = firstNonEmptyEnv("AGENT_MODEL_API_KEY", "MCT_MODEL_API_KEY")
            if c.cfg.AgentModelAPIKey != "" { warnLegacyOnce() }
        }
    }
    // Base URL
    if strings.TrimSpace(c.cfg.AgentModelBaseURL) == "" {
        c.cfg.AgentModelBaseURL = os.Getenv("OPENAI_BASE_URL")
        if strings.TrimSpace(c.cfg.AgentModelBaseURL) == "" {
            c.cfg.AgentModelBaseURL = firstNonEmptyEnv("AGENT_MODEL_BASE_URL", "MCT_MODEL_BASE_URL")
            if c.cfg.AgentModelBaseURL != "" { warnLegacyOnce() }
        }
    }
    return c
}

func firstNonEmptyEnv(keys ...string) string {
    for _, k := range keys {
        if v := strings.TrimSpace(os.Getenv(k)); v != "" {
            return v
        }
    }
    return ""
}

func warnLegacyOnce() {
    if warnedLegacyEnv { return }
    fmt.Fprintln(os.Stderr, "[deprecation] Using legacy AGENT_MODEL_* or MCT_MODEL_* envs. Please switch to OPENAI_API_KEY, OPENAI_BASE_URL, OPENAI_MODEL.")
    warnedLegacyEnv = true
}

// Plan decides the next action using only the transcript context.
func (c *Client) Plan(ctx context.Context, goal string, transcript string, step, maxSteps int) (Decision, string, error) {
    if c.cfg.DryRun {
        // First step: ask; later steps: finalize
        if step < maxSteps {
            return DecisionAsk, "From the transcript, ask mct for the next most informative repository-focused prompt.", nil
        }
        return DecisionFinalize, "", nil
    }
    prompt := c.planPrompt(goal, transcript, step, maxSteps)
    resp, err := c.chat(ctx, c.cfg.AgentModel, prompt)
    if err != nil {
        return "", "", err
    }
    dec, q := parseDecision(resp)
    if dec == "" {
        return "", "", errors.New("planner: unable to parse decision from model output")
    }
    return dec, q, nil
}

// Finalize composes the final answer using only the transcript content.
func (c *Client) Finalize(ctx context.Context, goal string, transcript string) (string, error) {
    if c.cfg.DryRun {
        return "[dry-run] Final answer would be composed here based on accumulated evidence.", nil
    }
    prompt := c.finalizePrompt(goal, transcript)
    resp, err := c.chat(ctx, c.cfg.AgentModel, prompt)
    if err != nil {
        return "", err
    }
    return strings.TrimSpace(resp), nil
}

func (c *Client) planPrompt(goal string, transcript string, step, maxSteps int) string {
    var b strings.Builder
    b.WriteString("You are an agentic planner for mct. Read the transcript to understand the goal and prior turns. mct reads repository files and answers; it does not execute code.\n")
    b.WriteString("Decide either to produce one single, high-signal repository-focused prompt (exactly one) or to finalize if enough information is gathered.\n")
    b.WriteString("Your prompt MUST be addressed to mct, not the user. Avoid clarifying user intent; focus on code, files, functions, modules, architecture, logs, or tests.\n")
    b.WriteString("Output strictly:\nDecision: ask|finalize\nIf ask, a second line using one of:\n- Question: <single best prompt>\n- Instruction: <single best prompt>\n- Message: <single best prompt>\n\n")
    if strings.TrimSpace(goal) != "" {
        b.WriteString("Goal:\n")
        // keep goal intact; it's short compared to transcript
        b.WriteString(goal + "\n\n")
    }
    if strings.TrimSpace(transcript) != "" {
        b.WriteString("Transcript (truncated):\n")
        tt := transcript
        if len(tt) > 4000 { tt = tt[len(tt)-4000:] }
        b.WriteString(tt + "\n\n")
    }
    b.WriteString(fmt.Sprintf("Step %d of %d. Decide.\n", step, maxSteps))
    return b.String()
}

func (c *Client) finalizePrompt(goal string, transcript string) string {
    var b strings.Builder
    b.WriteString("You are the composer agent. Read the transcript (which contains the goal and mct turns) and write the final answer to the original goal.\n\n")
    if strings.TrimSpace(goal) != "" {
        b.WriteString("Goal:\n")
        b.WriteString(goal + "\n\n")
    }
    if strings.TrimSpace(transcript) != "" {
        b.WriteString("Transcript (truncated):\n")
        tt := transcript
        if len(tt) > 6000 { tt = tt[len(tt)-6000:] }
        b.WriteString(tt + "\n\n")
    }
    b.WriteString("Now produce a clear, self-contained final answer grounded in the evidence from prior turns. If there are gaps, call them out succinctly.")
    return b.String()
}

// Minimal OpenAI-compatible chat client
func (c *Client) chat(ctx context.Context, model, prompt string) (string, error) {
    if strings.TrimSpace(model) == "" {
        model = strings.TrimSpace(c.cfg.AgentModel)
    }
    if strings.TrimSpace(model) == "" {
        return "", errors.New("missing model config: set --openai-model or OPENAI_MODEL")
    }
    if strings.TrimSpace(c.cfg.AgentModelBaseURL) == "" {
        return "", errors.New("missing base URL: set --openai-base-url or OPENAI_BASE_URL")
    }
    if strings.TrimSpace(c.cfg.AgentModelAPIKey) == "" {
        return "", errors.New("missing API key: set --openai-api-key or OPENAI_API_KEY")
    }
    reqBody := map[string]any{
        "model": model,
        "messages": []map[string]string{{
            "role":    "user",
            "content": prompt,
        }},
        "temperature": 0.2,
        "stream":      false,
    }
    data, _ := json.Marshal(reqBody)
    url := strings.TrimRight(c.cfg.AgentModelBaseURL, "/") + "/chat/completions"
    httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
    if err != nil {
        return "", err
    }
    httpReq.Header.Set("Content-Type", "application/json")
    if c.cfg.AgentModelAPIKey != "" {
        httpReq.Header.Set("Authorization", "Bearer "+c.cfg.AgentModelAPIKey)
    }
    resp, err := c.httpClient.Do(httpReq)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode < 200 || resp.StatusCode >= 300 {
        return "", fmt.Errorf("llm error: status %d", resp.StatusCode)
    }
    var out struct {
        Choices []struct {
            Message struct {
                Content string `json:"content"`
            } `json:"message"`
            Delta struct {
                Content string `json:"content"`
            } `json:"delta"`
        } `json:"choices"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return "", err
    }
    if len(out.Choices) == 0 {
        return "", errors.New("llm: no choices")
    }
    content := strings.TrimSpace(out.Choices[0].Message.Content)
    if content == "" { content = strings.TrimSpace(out.Choices[0].Delta.Content) }
    if content == "" {
        return "", errors.New("llm: empty content")
    }
    return content, nil
}

// parseDecision expects output with lines like:
// Decision: ask|finalize
// Question: ... (optional)
// Instruction: ... (optional)
// Message: ... (optional)
func parseDecision(s string) (Decision, string) {
    lines := strings.Split(strings.TrimSpace(s), "\n")
    var dec Decision
    var q string
    for _, l := range lines {
        t := strings.TrimSpace(l)
        if strings.HasPrefix(strings.ToLower(t), "decision:") {
            v := strings.TrimSpace(strings.TrimPrefix(t, "Decision:"))
            v = strings.ToLower(v)
            if strings.HasPrefix(v, "ask") { dec = DecisionAsk }
            if strings.HasPrefix(v, "finalize") { dec = DecisionFinalize }
        }
        lower := strings.ToLower(t)
        if strings.HasPrefix(lower, "question:") {
            q = strings.TrimSpace(strings.TrimPrefix(t, "Question:"))
        }
        if strings.HasPrefix(lower, "instruction:") {
            q = strings.TrimSpace(strings.TrimPrefix(t, "Instruction:"))
        }
        if strings.HasPrefix(lower, "message:") {
            q = strings.TrimSpace(strings.TrimPrefix(t, "Message:"))
        }
    }
    return dec, q
}
