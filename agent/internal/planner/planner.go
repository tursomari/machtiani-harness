package planner

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "net/http"
    "os"
    "strings"
    "time"
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
    // Optional per-request timeout in seconds; 0 disables client timeout (uses ctx only)
    RequestTimeoutSec int
}

type Client struct {
    cfg        ClientConfig
    httpClient *http.Client
}

var warnedLegacyEnv bool

func NewClient(cfg ClientConfig) *Client {
    hc := &http.Client{}
    if cfg.RequestTimeoutSec > 0 {
        hc.Timeout = time.Duration(cfg.RequestTimeoutSec) * time.Second
    }
    c := &Client{cfg: cfg, httpClient: hc}
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
        "temperature": 1,
        "stream":      false,
    }
    data, _ := json.Marshal(reqBody)
    // Build endpoint path safely: allow base to already include /chat/completions
    base := strings.TrimRight(c.cfg.AgentModelBaseURL, "/")
    endpoint := base
    if !strings.HasSuffix(strings.ToLower(base), "/chat/completions") {
        endpoint = base + "/chat/completions"
    }

    // Up to 2 retries on transient errors (total 3 attempts)
    var lastErr error
    for attempt := 1; attempt <= 3; attempt++ {
        if c.cfg.Verbose {
            fmt.Fprintf(os.Stderr, "[planner] POST %s (model=%s, attempt=%d)\n", endpoint, model, attempt)
        }
        httpReq, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(data))
        if err != nil {
            return "", err
        }
        httpReq.Header.Set("Content-Type", "application/json")
        if c.cfg.AgentModelAPIKey != "" {
            httpReq.Header.Set("Authorization", "Bearer "+c.cfg.AgentModelAPIKey)
        }
        // Optional OpenRouter-friendly headers if provided by user
        if v := strings.TrimSpace(os.Getenv("OPENROUTER_REFERER")); v != "" {
            httpReq.Header.Set("HTTP-Referer", v)
        }
        if v := strings.TrimSpace(os.Getenv("OPENROUTER_TITLE")); v != "" {
            httpReq.Header.Set("X-Title", v)
        }
        httpReq.Header.Set("User-Agent", "mct-agent/1 (planner)")

        resp, err := c.httpClient.Do(httpReq)
        if err != nil {
            if isRetryableNetErr(err) && ctx.Err() == nil && attempt < 3 {
                time.Sleep(time.Duration(250*attempt) * time.Millisecond)
                lastErr = err
                continue
            }
            return "", err
        }
        defer resp.Body.Close()
        if resp.StatusCode < 200 || resp.StatusCode >= 300 {
            // Read a limited body to extract structured error info when available.
            var bodyPreview string
            if resp.Body != nil {
                lr := io.LimitReader(resp.Body, 16*1024)
                if b, _ := io.ReadAll(lr); len(b) > 0 {
                    bodyPreview = strings.TrimSpace(string(b))
                }
            }
            // Try parse OpenAI-compatible error shape
            errStr := formatLLMHTTPError(resp.StatusCode, bodyPreview)
            if (resp.StatusCode == 429 || resp.StatusCode >= 500) && attempt < 3 {
                time.Sleep(time.Duration(300*attempt) * time.Millisecond)
                lastErr = errors.New(errStr)
                continue
            }
            return "", errors.New(errStr)
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
            if attempt < 3 {
                lastErr = err
                time.Sleep(time.Duration(200*attempt) * time.Millisecond)
                continue
            }
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
    if lastErr != nil {
        return "", lastErr
    }
    return "", errors.New("planner chat: unknown error")
}

// isRetryableNetErr returns true for transient network/context errors worth retrying.
func isRetryableNetErr(err error) bool {
    s := strings.ToLower(err.Error())
    if strings.Contains(s, "timeout") || strings.Contains(s, "deadline") || strings.Contains(s, "eof") || strings.Contains(s, "reset") || strings.Contains(s, "temporary") {
        return true
    }
    return false
}

// formatLLMHTTPError produces a provider-agnostic error string from an HTTP status
// and an optional response body. It attempts to parse OpenAI-compatible shapes and
// falls back to the raw body when parsing fails.
func formatLLMHTTPError(status int, body string) string {
    type openAIError struct {
        Error struct {
            Message string      `json:"message"`
            Type    string      `json:"type"`
            Code    interface{} `json:"code"`
            Param   interface{} `json:"param"`
        } `json:"error"`
        Message      string      `json:"message"`
        ErrorMessage string      `json:"error_message"`
        Detail       interface{} `json:"detail"`
    }

    var msg, typ, code, detail string
    // Best-effort JSON parse
    if strings.HasPrefix(strings.TrimSpace(body), "{") {
        var e openAIError
        if json.Unmarshal([]byte(body), &e) == nil {
            if m := strings.TrimSpace(e.Error.Message); m != "" {
                msg = m
            }
            if t := strings.TrimSpace(e.Error.Type); t != "" {
                typ = t
            }
            if e.Error.Code != nil {
                code = strings.TrimSpace(fmt.Sprint(e.Error.Code))
            }
            if msg == "" {
                if m := strings.TrimSpace(e.Message); m != "" {
                    msg = m
                }
            }
            if msg == "" {
                if m := strings.TrimSpace(e.ErrorMessage); m != "" {
                    msg = m
                }
            }
            if e.Detail != nil {
                detail = strings.TrimSpace(fmt.Sprint(e.Detail))
            }
        }
    }

    parts := []string{fmt.Sprintf("llm error: status %d", status)}
    if typ != "" {
        parts = append(parts, fmt.Sprintf("type=%s", typ))
    }
    if code != "" {
        parts = append(parts, fmt.Sprintf("code=%s", code))
    }
    if msg != "" {
        parts = append(parts, fmt.Sprintf("message=\"%s\"", truncateMiddle(msg, 800)))
    } else if strings.TrimSpace(body) != "" {
        parts = append(parts, fmt.Sprintf("body=\"%s\"", truncateMiddle(strings.TrimSpace(body), 800)))
    }
    if detail != "" && msg == "" {
        parts = append(parts, fmt.Sprintf("detail=\"%s\"", truncateMiddle(detail, 800)))
    }
    return strings.Join(parts, ": ")
}

func truncateMiddle(s string, max int) string {
    if max <= 0 || len(s) <= max {
        return s
    }
    if max <= 10 {
        return s[:max]
    }
    head := max/2 - 3
    tail := max - head - 6
    if head < 0 {
        head = 0
    }
    if tail < 0 {
        tail = 0
    }
    if head+tail+6 > len(s) {
        // fallback sane truncate
        return s[:max]
    }
    return s[:head] + "[...]" + s[len(s)-tail:]
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
