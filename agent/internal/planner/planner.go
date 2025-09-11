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

func NewClient(cfg ClientConfig) *Client {
    c := &Client{cfg: cfg, httpClient: &http.Client{}}
    if c.cfg.AgentModelBaseURL == "" {
        c.cfg.AgentModelBaseURL = "https://api.openai.com/v1"
    }
    return c
}

type Turn struct {
    Step               int
    Question           string
    RetrievedFilePaths []string
    AnswerExcerpt      string
    SavedChatPath      string
}

func (c *Client) Plan(ctx context.Context, goal, summary, lastAnswer string, retrieved []string, step, maxSteps int) (Decision, string, error) {
    if c.cfg.DryRun {
        // First step: ask; later steps: finalize
        if step < maxSteps {
            return DecisionAsk, fmt.Sprintf("Given the goal: %s — what files and functions are most relevant?", goal), nil
        }
        return DecisionFinalize, "", nil
    }
    prompt := c.planPrompt(goal, summary, lastAnswer, retrieved, step, maxSteps)
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

func (c *Client) Finalize(ctx context.Context, goal string, evidence []Turn, summary string) (string, error) {
    if c.cfg.DryRun {
        return "[dry-run] Final answer would be composed here based on accumulated evidence.", nil
    }
    prompt := c.finalizePrompt(goal, evidence, summary)
    resp, err := c.chat(ctx, c.cfg.AgentModel, prompt)
    if err != nil {
        return "", err
    }
    return strings.TrimSpace(resp), nil
}

func (c *Client) planPrompt(goal, summary, lastAnswer string, retrieved []string, step, maxSteps int) string {
    var b strings.Builder
    b.WriteString("You are an agentic planner whose message is sent to the mct tool, not a human. mct cannot run code; it only reads repository files and answers about them.\n")
    b.WriteString("Decide either to produce one single, high-signal repository-focused prompt (exactly one) or to finalize if enough information is gathered.\n")
    b.WriteString("Your prompt MUST be addressed to mct (the codebase-aware tool), not the user. Do NOT ask to clarify user intent, preferences, or scope.\n")
    b.WriteString("Focus on files, functions, modules, architecture, error logs, or tests. Do not imply command execution.\n")
    b.WriteString("Output strictly:\nDecision: ask|finalize\nIf ask, a second line using one of:\n- Question: <single best prompt>\n- Instruction: <single best prompt>\n- Message: <single best prompt>\n\n")
    b.WriteString("Original goal: \n" + goal + "\n\n")
    if summary != "" {
        b.WriteString("Running summary:\n" + summary + "\n\n")
    }
    if lastAnswer != "" {
        b.WriteString("Last mct answer excerpt (truncated):\n")
        // cheap truncate to keep prompt compact
        la := lastAnswer
        if len(la) > 1200 { la = la[:1200] }
        b.WriteString(la + "\n\n")
    }
    if len(retrieved) > 0 {
        b.WriteString("Retrieved File Paths (last turn):\n- " + strings.Join(retrieved, "\n- ") + "\n\n")
    }
    b.WriteString(fmt.Sprintf("Step %d of %d. Decide.\n", step, maxSteps))
    return b.String()
}

func (c *Client) finalizePrompt(goal string, evidence []Turn, summary string) string {
    var b strings.Builder
    b.WriteString("You are the composer agent. Using the accumulated evidence and summaries from previous mct turns, write the final answer to the original goal.\n\n")
    b.WriteString("Original goal:\n" + goal + "\n\n")
    if summary != "" { b.WriteString("Running summary:\n" + summary + "\n\n") }
    b.WriteString("Evidence log (per turn):\n")
    for _, t := range evidence {
        b.WriteString(fmt.Sprintf("Turn %d question: %s\n", t.Step, t.Question))
        if len(t.RetrievedFilePaths) > 0 {
            b.WriteString("Files:\n- " + strings.Join(t.RetrievedFilePaths, "\n- ") + "\n")
        }
        if t.AnswerExcerpt != "" {
            b.WriteString("Answer excerpt:\n" + t.AnswerExcerpt + "\n")
        }
        b.WriteString("\n")
    }
    b.WriteString("Now, produce a clear, self-contained final answer. If there are gaps due to limited steps, call them out succinctly.")
    return b.String()
}

// Minimal OpenAI-compatible chat client
func (c *Client) chat(ctx context.Context, model, prompt string) (string, error) {
    if model == "" {
        model = os.Getenv("AGENT_MODEL")
        if model == "" {
            model = "gpt-4o-mini"
        }
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
