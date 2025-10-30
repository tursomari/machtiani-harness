package planner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

type Decision string

const (
	DecisionAsk      Decision = "ask"
	DecisionPatch    Decision = "patch"
	DecisionFinalize Decision = "finalize"
)

type ClientConfig struct {
	Model             llm.ResolvedModel
	Extras            map[string]any
	Alias             string
	Verbose           bool
	DryRun            bool
	RequestTimeoutSec int
	PatchEnabled      bool
}

type Client struct {
	cfg ClientConfig
}

func NewClient(cfg ClientConfig) *Client {
	if cfg.Extras == nil {
		cfg.Extras = map[string]any{}
	}
	return &Client{cfg: cfg}
}

// Plan decides the next action using only the transcript context.
func (c *Client) Plan(ctx context.Context, goal string, transcript string, step, maxSteps int) (Decision, string, error) {
	if c.cfg.DryRun {
		if step < maxSteps {
			return DecisionAsk, "From the transcript, ask mct for the next most informative repository-focused prompt.", nil
		}
		return DecisionFinalize, "", nil
	}
	prompt := c.planPrompt(goal, transcript, step, maxSteps)
	w, hasWriter := trajectory.FromContext(ctx)
	parentSpan, _ := trajectory.ParentSpanID(ctx)
	chatCtx := ctx
	var span trajectory.Span
	if hasWriter {
		span = w.StartSpan(parentSpan)
		payload := map[string]any{
			"event_version": 1,
			"model_alias":   c.cfg.Alias,
			"model_name":    c.cfg.Model.Model,
			"step":          step,
			"max_steps":     maxSteps,
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(prompt, w.ExcerptLen()), "prompt")
		if err := w.Emit(ctx, trajectory.Event{Kind: "planner.request", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}); err != nil {
			reportTrajectoryError(err)
		}
		chatCtx = trajectory.ContextWithParentSpan(ctx, span.ID)
	}
	start := time.Now()
	resp, err := c.chat(chatCtx, prompt)
	duration := time.Since(start)
	if err != nil {
		if hasWriter {
			payload := map[string]any{
				"event_version": 1,
				"model_alias":   c.cfg.Alias,
				"model_name":    c.cfg.Model.Model,
				"step":          step,
				"max_steps":     maxSteps,
				"duration_ms":   duration.Milliseconds(),
				"parse_ok":      false,
			}
			category, code := llm.ClassifyError(err)
			evt := trajectory.Event{
				Level:        "error",
				Kind:         "planner.response",
				SpanID:       span.ID,
				ParentSpanID: parentSpan,
				Payload:      payload,
				Err: &trajectory.ErrorInfo{
					Message:  err.Error(),
					Category: category,
					Code:     code,
				},
			}
			if emitErr := w.Emit(ctx, evt); emitErr != nil {
				reportTrajectoryError(emitErr)
			}
		}
		return "", "", err
	}
	if c.cfg.Verbose {
		fmt.Fprintln(os.Stderr, "[planner] model response:", truncateMiddle(strings.TrimSpace(resp), 1800))
	}
	dec, q := parseDecision(resp, c.cfg.PatchEnabled)
	if hasWriter {
		payload := map[string]any{
			"event_version": 1,
			"model_alias":   c.cfg.Alias,
			"model_name":    c.cfg.Model.Model,
			"step":          step,
			"max_steps":     maxSteps,
			"duration_ms":   duration.Milliseconds(),
			"parse_ok":      dec != "",
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(prompt, w.ExcerptLen()), "prompt")
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(resp, w.ExcerptLen()), "response")
		if dec != "" {
			payload["parse"] = map[string]any{"decision": string(dec)}
		}
		level := "info"
		var errInfo *trajectory.ErrorInfo
		if dec == "" {
			level = "warn"
			errInfo = &trajectory.ErrorInfo{Message: "unable to parse decision from model output", Category: "parse"}
		}
		evt := trajectory.Event{
			Level:        level,
			Kind:         "planner.response",
			SpanID:       span.ID,
			ParentSpanID: parentSpan,
			Payload:      payload,
			Err:          errInfo,
		}
		if emitErr := w.Emit(ctx, evt); emitErr != nil {
			reportTrajectoryError(emitErr)
		}
	}
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
	w, hasWriter := trajectory.FromContext(ctx)
	parentSpan, _ := trajectory.ParentSpanID(ctx)
	callCtx := ctx
	var span trajectory.Span
	if hasWriter {
		span = w.StartSpan(parentSpan)
		payload := map[string]any{
			"event_version": 1,
			"model_alias":   c.cfg.Alias,
			"model_name":    c.cfg.Model.Model,
			"operation":     "finalize",
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(prompt, w.ExcerptLen()), "prompt")
		if err := w.Emit(ctx, trajectory.Event{Kind: "planner.request", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}); err != nil {
			reportTrajectoryError(err)
		}
		callCtx = trajectory.ContextWithParentSpan(ctx, span.ID)
	}
	start := time.Now()
	resp, err := c.chat(callCtx, prompt)
	duration := time.Since(start)
	if err != nil {
		if hasWriter {
			payload := map[string]any{
				"event_version": 1,
				"model_alias":   c.cfg.Alias,
				"model_name":    c.cfg.Model.Model,
				"operation":     "finalize",
				"duration_ms":   duration.Milliseconds(),
				"parse_ok":      false,
			}
			category, code := llm.ClassifyError(err)
			evt := trajectory.Event{
				Level:        "error",
				Kind:         "planner.response",
				SpanID:       span.ID,
				ParentSpanID: parentSpan,
				Payload:      payload,
				Err: &trajectory.ErrorInfo{
					Message:  err.Error(),
					Category: category,
					Code:     code,
				},
			}
			if emitErr := w.Emit(ctx, evt); emitErr != nil {
				reportTrajectoryError(emitErr)
			}
		}
		return "", err
	}
	trimmed := strings.TrimSpace(resp)
	if hasWriter {
		payload := map[string]any{
			"event_version": 1,
			"model_alias":   c.cfg.Alias,
			"model_name":    c.cfg.Model.Model,
			"operation":     "finalize",
			"duration_ms":   duration.Milliseconds(),
			"parse_ok":      true,
		}
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(prompt, w.ExcerptLen()), "prompt")
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(resp, w.ExcerptLen()), "response")
		payload["parse"] = map[string]any{"decision": "finalize"}
		evt := trajectory.Event{
			Kind:         "planner.response",
			SpanID:       span.ID,
			ParentSpanID: parentSpan,
			Payload:      payload,
		}
		if emitErr := w.Emit(ctx, evt); emitErr != nil {
			reportTrajectoryError(emitErr)
		}
	}
	return trimmed, nil
}

func (c *Client) chat(ctx context.Context, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("planner: prompt must not be empty")
	}
	messages := []llm.Message{{Role: "user", Content: prompt}}
	callCtx := ctx
	var cancel context.CancelFunc
	if c.cfg.RequestTimeoutSec > 0 {
		callCtx, cancel = context.WithTimeout(ctx, time.Duration(c.cfg.RequestTimeoutSec)*time.Second)
		defer cancel()
	}
	return llm.ChatWithResolved(callCtx, c.cfg.Model, c.cfg.Extras, messages)
}

func (c *Client) planPrompt(goal string, transcript string, step, maxSteps int) string {
	var b strings.Builder
	b.WriteString("You are an agentic planner for mct. Read the transcript to understand the goal and prior turns. mct reads repository files and answers; it does not execute code.\n")
	b.WriteString("Patch validation diagnostics are recorded in the transcript; use them to decide on next steps when patches fail.\n")
	if !c.cfg.PatchEnabled {
		b.WriteString("Patch requests are disabled for this run. Decide either to: (a) produce one single, high-signal repository-focused prompt, or (b) finalize if enough information is gathered.\n")
	} else {
		b.WriteString("Decide either to: (a) produce one single, high-signal repository-focused prompt, (b) request a patch, or (c) finalize if enough information is gathered.\n")
	}
	b.WriteString("Your prompt MUST be addressed to mct, not the user. Avoid clarifying user intent; focus on code, files, functions, modules, architecture, logs, or tests.\n")
	b.WriteString("Output strictly:\nDecision: ")
	if !c.cfg.PatchEnabled {
		b.WriteString("ask|finalize\n")
	} else {
		b.WriteString("ask|patch|finalize\n")
	}
	b.WriteString("If ask, a second line using exactly one of:\n- Question: <single best prompt>\n- Instruction: <single best prompt>\n- Message: <single best prompt>\n")
	if c.cfg.PatchEnabled {
		b.WriteString("If patch, immediately follow with a single standalone JSON object ONLY (no commentary, no markdown fences).\n\n")
		b.WriteString("Patch JSON schema (when Decision: patch):\n")
		b.WriteString("{\n  \"metadata\": { \"description\": string, \"author\": string, \"email\": string },\n  \"edits\": [\n    { \"path\": string (repo-relative), \"mode\": one of replace|rewrite|create|delete,\n      \"before\": string (replace only), \"after\": string (replace only), \"occurrence\": number (1-based, optional),\n      \"new_content\": string (rewrite/create only) }\n  ]\n}\n")
		b.WriteString("Rules: use forward slashes; paths must be under repo root;\n")
		b.WriteString("replace requires before+after and file exists; rewrite requires new_content and file exists;\n")
		b.WriteString("create requires new_content and file must not exist; delete requires file exists.\n\n")
		b.WriteString("Minimal example (do not include this text in output):\n")
		b.WriteString("Decision: patch\n{\n  \"metadata\": { \"description\": \"Fix README typo\" },\n  \"edits\": [\n    { \"path\": \"README.md\", \"mode\": \"replace\", \"before\": \"teh\", \"after\": \"the\", \"occurrence\": 1 }\n  ]\n}\n\n")
	}

	if strings.TrimSpace(goal) != "" {
		b.WriteString("Goal:\n")
		b.WriteString(goal + "\n\n")
	}
	if strings.TrimSpace(transcript) != "" {
		b.WriteString("Transcript:\n")
		b.WriteString(transcript + "\n\n")
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
		b.WriteString("Transcript:\n")
		b.WriteString(transcript + "\n\n")
	}
	b.WriteString("Now produce a clear, self-contained final answer grounded in the evidence from prior turns. If there are gaps, call them out succinctly.")
	return b.String()
}

func truncateMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + "…" + s[len(s)-half:]
}

func parseDecision(resp string, patchEnabled bool) (Decision, string) {
	lines := strings.Split(strings.TrimSpace(resp), "\n")
	if len(lines) == 0 {
		return "", ""
	}
	line := strings.TrimSpace(lines[0])
	if !strings.HasPrefix(strings.ToLower(line), "decision:") {
		return "", ""
	}
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	decisionStr := strings.TrimSpace(strings.ToLower(parts[1]))
	var decision Decision
	switch decisionStr {
	case string(DecisionAsk), "question", "instruction", "message":
		decision = DecisionAsk
	case string(DecisionPatch):
		decision = DecisionPatch
	case string(DecisionFinalize):
		decision = DecisionFinalize
	default:
		return "", ""
	}
	if !patchEnabled && decision == DecisionPatch {
		// Fallback to a generic ask when patches are disabled to keep the agent progressing.
		return DecisionAsk, "Question: Considering the current transcript, produce the single next high-signal repository-focused prompt for mct."
	}
	remainder := strings.TrimSpace(strings.Join(lines[1:], "\n"))
	return decision, remainder
}

func reportTrajectoryError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[trajectory] emit error: %v\n", err)
}
