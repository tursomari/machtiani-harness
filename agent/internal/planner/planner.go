package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/patchlog"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

type Decision string

const (
	DecisionAsk      Decision = "ask"
	DecisionPatch    Decision = "patch"
	DecisionFinalize Decision = "finalize"
	DecisionAccept   Decision = "accept"
	DecisionReject   Decision = "reject"
)

const (
	successFilesPromptLimit = 12
	progressMaxTrackedFiles = 100
)

type ClientConfig struct {
	Model             llm.ResolvedModel
	Extras            map[string]any
	Alias             string
	Verbose           bool
	DryRun            bool
	RequestTimeoutSec int
	PatchEnabled      bool
	StrictPatchMode   bool
	PatchFull         bool
	RepoRoot          string
	SessionID         string
}

type Client struct {
	cfg      ClientConfig
	chatFn   func(context.Context, string) (string, error)
	progress Progress
}

type Progress struct {
	SuccessFiles        []string
	AppliedPatches      int
	ForceRepatchExample bool
	PendingReview       *PendingReview
}

// PendingReview captures metadata about the most recent patch awaiting
// acceptance so the planner can decide whether to keep or undo it.
type PendingReview struct {
	PatchPath        string   `json:"patch_path"`
	ReversePatchPath string   `json:"reverse_patch_path"`
	Description      string   `json:"description,omitempty"`
	Files            []string `json:"files,omitempty"`
	Sequence         int      `json:"sequence,omitempty"`
	Insertions       int      `json:"insertions,omitempty"`
	Deletions        int      `json:"deletions,omitempty"`
}

// Clone returns a deep copy of the pending review metadata.
func (p *PendingReview) Clone() *PendingReview {
	if p == nil {
		return nil
	}
	clone := *p
	if len(p.Files) > 0 {
		clone.Files = append([]string(nil), p.Files...)
	}
	return &clone
}

func NewClient(cfg ClientConfig) *Client {
	if cfg.Extras == nil {
		cfg.Extras = map[string]any{}
	}
	return &Client{cfg: cfg}
}

// UpdateProgress refreshes planner-aware session progress (e.g. prior strict
// patch successes) so prompts can steer the model away from redundant work.
func (c *Client) UpdateProgress(progress Progress) {
	seen := make(map[string]struct{})
	files := make([]string, 0, len(progress.SuccessFiles))
	limit := progressMaxTrackedFiles
	for _, raw := range progress.SuccessFiles {
		norm := normalizeProgressPath(raw)
		if norm == "" {
			continue
		}
		if _, exists := seen[norm]; exists {
			continue
		}
		seen[norm] = struct{}{}
		files = append(files, norm)
		if len(files) == limit {
			break
		}
	}
	applied := progress.AppliedPatches
	if applied < 0 {
		applied = 0
	}
	c.progress = Progress{
		SuccessFiles:        files,
		AppliedPatches:      applied,
		ForceRepatchExample: progress.ForceRepatchExample,
		PendingReview:       progress.PendingReview.Clone(),
	}
}

func normalizeProgressPath(path string) string {
	return filepath.ToSlash(strings.TrimSpace(path))
}

func appendSuccessFilesSection(b *strings.Builder, files []string, intro string, limit int) {
	if len(files) == 0 {
		return
	}
	b.WriteString(intro)
	appendSuccessFilesList(b, files, limit)
	b.WriteString("\n")
}

func appendSuccessFilesList(b *strings.Builder, files []string, limit int) {
	if limit <= 0 || limit > len(files) {
		limit = len(files)
	}
	for i := 0; i < limit; i++ {
		b.WriteString("- ")
		b.WriteString(files[i])
		b.WriteString("\n")
	}
	if len(files) > limit {
		fmt.Fprintf(b, "- … (%d more)\n", len(files)-limit)
	}
}

// Plan decides the next action using only the transcript context.
func (c *Client) Plan(ctx context.Context, goal string, transcript string, step, maxSteps int) (Decision, string, error) {
	if c.cfg.DryRun {
		if step < maxSteps {
			return DecisionAsk, "From the transcript, ask mct for the next most informative repository-focused prompt.", nil
		}
		return DecisionFinalize, "", nil
	}
	reviewMode := c.progress.PendingReview != nil
	var prompt string
	if reviewMode {
		prompt = c.reviewPrompt(goal, transcript, step, maxSteps)
	} else {
		prompt = c.planPrompt(goal, transcript, step, maxSteps)
	}
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
	dec, q, preamble := parseDecision(resp, c.cfg.PatchEnabled)
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
		if preamble != "" {
			payload["ignored_preamble"] = trajectory.MakeTextExcerpt(preamble, w.ExcerptLen())
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
	if reviewMode {
		if dec != DecisionAccept && dec != DecisionReject {
			return "", "", errors.New("planner: expected accept or reject decision for pending patch review")
		}
		return dec, q, nil
	}
	if dec == DecisionPatch && c.cfg.StrictPatchMode {
		if !shouldUseStrictPatchMode(q) {
			if c.cfg.Verbose {
				fmt.Fprintln(os.Stderr, "[planner] detected rewrite in plan; bypassing strict mode")
			}
			c.cfg.StrictPatchMode = false
			c.cfg.PatchFull = true
		} else {
			strictPayload, err := c.runStrictPatchFlow(ctx, goal, transcript, step, maxSteps, q)
			if err != nil {
				if isRewriteNotSupportedError(err) {
					if c.cfg.Verbose {
						fmt.Fprintln(os.Stderr, "[planner] strict patch rejected rewrite; rerouting to full-mode patch flow")
					}
					// Reroute: disable strict mode, enable full mode, and retry.
					c.cfg.StrictPatchMode = false
					c.cfg.PatchFull = true
					// Re-invoke the strict patch flow (which now allows rewrites via PatchFull).
					strictPayload, err = c.runStrictPatchFlow(ctx, goal, transcript, step, maxSteps, q)
				}
			}

			if err != nil {
				if c.cfg.Verbose {
					fmt.Fprintln(os.Stderr, "[planner] strict patch failed:", truncateMiddle(err.Error(), 160))
				}
				if hasWriter {
					reason := sanitizeForPrompt(err.Error())
					payload := map[string]any{
						"event_version": 1,
						"model_alias":   c.cfg.Alias,
						"model_name":    c.cfg.Model.Model,
						"step":          step,
						"max_steps":     maxSteps,
						"parse_ok":      false,
						"error":         reason,
					}
					evt := trajectory.Event{
						Level:        "warn",
						Kind:         "planner.strict_patch.error",
						SpanID:       span.ID,
						ParentSpanID: parentSpan,
						Payload:      payload,
						Err: &trajectory.ErrorInfo{
							Message:  reason,
							Category: "strict_patch",
						},
					}
					if emitErr := w.Emit(ctx, evt); emitErr != nil {
						reportTrajectoryError(emitErr)
					}
				}
				dec = DecisionAsk
				q = strictPatchFallbackQuestion(err)
			} else {
				q = strictPayload
			}
		}
	}
	if dec == DecisionPatch {
		meta := patchlog.Metadata{
			Source: "planner.plan",
			Model:  strings.TrimSpace(c.cfg.Model.Model),
			Alias:  strings.TrimSpace(c.cfg.Alias),
			Step:   step,
		}
		if path, err := patchlog.WritePrompt(prompt, meta); err != nil {
			if c.cfg.Verbose {
				fmt.Fprintf(os.Stderr, "[planner] failed to write patch prompt log: %v\n", err)
			}
		} else if c.cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[planner] patch prompt logged to %s\n", path)
		}
	}
	return dec, q, nil
}

func strictPatchFallbackQuestion(err error) string {
	reason := sanitizeForPrompt(err.Error())
	if reason == "" {
		reason = "strict patch attempt failed"
	}
	return "Strict patch attempt failed (" + reason + "). Retrieve the exact numbered snippet for the referenced file so we can regenerate a valid patch."
}

func isRewriteNotSupportedError(err error) bool {
	// We check the error string because the error type is defined in strict_patch_planner.go
	// but we want to avoid circular dependencies if strict_patch_planner.go was in a subpackage.
	// However, they are in the same package 'planner'.
	// So we can check the type directly if it's exported.
	var rewriteErr *ErrRewriteNotSupported
	return errors.As(err, &rewriteErr)
}

func shouldUseStrictPatchMode(payloadJSON string) bool {
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return true
	}
	editsVal, ok := payload["edits"]
	if !ok {
		return true
	}
	editsSlice, ok := editsVal.([]any)
	if !ok {
		return true
	}
	for _, edit := range editsSlice {
		editMap, ok := edit.(map[string]any)
		if !ok {
			continue
		}
		mode, _ := editMap["mode"].(string)
		if strings.ToLower(strings.TrimSpace(mode)) == "rewrite" {
			return false
		}
	}
	return true
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
	if c.chatFn != nil {
		return c.chatFn(ctx, prompt)
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
	b.WriteString("Begin your reply immediately with `Decision:`—no commentary, whitespace, or reasoning before it.\n")
	b.WriteString("If you need to reason, do it silently; any text before `Decision:` causes the run to fail.\n")
	b.WriteString("Output strictly:\nDecision: ")
	if !c.cfg.PatchEnabled {
		b.WriteString("ask|finalize\n")
	} else {
		b.WriteString("ask|patch|finalize\n")
	}
	b.WriteString("If ask, a second line using exactly one of:\n- Question: <single best prompt>\n- Instruction: <single best prompt>\n- Message: <single best prompt>\n")
	if c.cfg.PatchEnabled {
		b.WriteString("If patch, immediately follow with a single standalone JSON object ONLY (no commentary, no markdown fences).\n\n")
		if c.cfg.StrictPatchMode {
			b.WriteString("Patch JSON schema (when Decision: patch):\n")
			b.WriteString("{\n  \"edits\": [\n    { \"path\": string, \"mode\": \"create\"|\"delete\"|\"rewrite\", \"new_content\": string (rewrite/create only) },\n    { \"path\": string, \"mode\": \"replace\", \"before\": string, \"after\": string, \"occurrence\": number (optional) },\n    { \"path\": string, \"mode\": \"patch\", \"patch\": { \"hunks\": [ { \"old_start\": int, \"old_count\": int, \"new_start\": int, \"new_count\": int, \"context_before\": [string], \"deletions\": [string], \"additions\": [string], \"context_after\": [string], \"snippet_source\": { \"start_line\": int, \"end_line\": int, \"filepath\": string (optional) } } ] } }\n  ],\n  \"metadata\": { \"description\": string (optional) }\n}\n")
			b.WriteString("Mode guidelines:\n")
			b.WriteString("- create: provide new_content; file must be absent.\n")
			b.WriteString("- delete: target file must exist.\n")
			b.WriteString("- rewrite: provide new_content for full-file replacement.\n")
			b.WriteString("- replace: legacy nth-occurrence substitution; use only when unavoidable.\n")
			b.WriteString("- patch: strict context-anchored hunks; use for precise edits to existing files.\n")
			b.WriteString("Patch mode rules:\n")
			b.WriteString("  * Hunks must include exact context_before/context_after lines from the file.\n")
			b.WriteString("  * Each hunk must include snippet_source with 1-based inclusive start_line and end_line for the before snippet; omit filepath to default to the edit path.\n")
			b.WriteString("  * old_start/new_start use 1-based line numbers from the current file.\n")
			b.WriteString("  * If any context does not match byte-for-byte, the patch fails with diagnostics—regenerate using the real file content shown in the transcript.\n")
			b.WriteString("  * Do not rely on fuzzy matching or omit context; each hunk applies deterministically.\n\n")
			b.WriteString("Strict patch planner flow:\n")
			b.WriteString("  1. Prompt the planner LLM with the goal/context to select the exact repo-relative file path that needs editing; parse that path from the reply.\n")
			b.WriteString("  2. Load that file directly from disk using the resolved path so you have the authoritative contents with line numbers.\n")
			b.WriteString("  3. Modify the in-memory copy with the lines you want inserted, removed, or rewritten.\n")
			b.WriteString("  4. Populate the JSON schema from those concrete lines, including snippet_source start/end that match the before snippet exactly.\n")
			b.WriteString("  5. If the file changes later in the run, reload it from disk before producing the final patch.\n\n")
			b.WriteString("Minimal example (do not include this text in output):\n")
			b.WriteString("Decision: patch\n{\n  \"edits\": [\n    { \"path\": \"docs/guide.md\", \"mode\": \"patch\", \"patch\": {\n      \"hunks\": [{\n        \"old_start\": 12, \"old_count\": 3, \"new_start\": 12, \"new_count\": 3,\n        \"context_before\": [\"## Overview\"],\n        \"deletions\": [\"This feautre is experimental.\"],\n        \"additions\": [\"This feature is experimental.\"],\n        \"context_after\": [\"Use with caution.\"],\n        \"snippet_source\": { \"start_line\": 12, \"end_line\": 14 }\n      }]\n    } }\n  ],\n  \"metadata\": { \"description\": \"Fix typo\" }\n}\n\n")
			b.WriteString("When intentionally re-editing a file already updated this session, reload it from disk first and set metadata.force_repatch to true.\n\n")
		} else {
			b.WriteString("Patch JSON schema (when Decision: patch):\n")
			b.WriteString("{\n  \"edits\": [\n    { \"path\": string (repo-relative), \"mode\": one of replace|rewrite|create|delete,\n      \"before\": string (replace only), \"after\": string (replace only), \"occurrence\": number (1-based, optional),\n      \"new_content\": string (rewrite/create only) }\n  ],\n  \"metadata\": { \"description\": string (optional) }\n}\n")
			b.WriteString("Rules: use forward slashes; paths must be under repo root;\n")
			b.WriteString("replace requires before+after and file exists; rewrite requires new_content and file exists;\n")
			b.WriteString("create requires new_content and file must not exist; delete requires file exists.\n\n")
			b.WriteString("Minimal example (do not include this text in output):\n")
			b.WriteString("Decision: patch\n{\n  \"edits\": [\n    { \"path\": \"README.md\", \"mode\": \"replace\", \"before\": \"teh\", \"after\": \"the\", \"occurrence\": 1 }\n  ],\n  \"metadata\": { \"description\": \"Fix README typo\" }\n}\n\n")
			b.WriteString("When intentionally re-editing a file already updated this session, reload it from disk first and set metadata.force_repatch to true.\n\n")
		}
		c.appendForceRepatchExample(&b)
	}

	appendSuccessFilesSection(&b, c.progress.SuccessFiles, "Files already updated successfully this session (reload these paths before considering further edits; prefer new targets. If you must revisit one, set metadata.force_repatch: true):\n", successFilesPromptLimit)
	if c.progress.AppliedPatches > 0 {
		fmt.Fprintf(&b, "Strict patch successes so far: %d. Avoid redundant patches—finalize once all required files are complete.\n\n", c.progress.AppliedPatches)
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

func (c *Client) reviewPrompt(goal string, transcript string, step, maxSteps int) string {
	review := c.progress.PendingReview
	var b strings.Builder
	b.WriteString("A patch was just applied. Meticulously examine the diff to ensure every change is correct and remains within the stated goals—reject if you spot unnecessary additions, removals, or other scope creep.\n")
	b.WriteString("Accept keeps the changes. Reject applies the undo patch to revert them.\n")
	b.WriteString("Begin your reply immediately with `Decision:`—no leading commentary.\n")
	b.WriteString("Allowed values: accept or reject (case-insensitive).\n")
	b.WriteString("Always include a second line formatted `Reason: <brief justification>` that cites why the changes are correct and in-scope (even if you accept).\n\n")
	if review != nil {
		if desc := strings.TrimSpace(review.Description); desc != "" {
			b.WriteString("Patch summary: " + sanitizeForPrompt(desc) + "\n")
		}
		if review.Sequence > 0 {
			fmt.Fprintf(&b, "Patch sequence: %d\n", review.Sequence)
		}
		if review.Insertions != 0 || review.Deletions != 0 {
			fmt.Fprintf(&b, "Diff stats: +%d / -%d\n", review.Insertions, review.Deletions)
		}
		if len(review.Files) > 0 {
			b.WriteString("Files modified:\n")
			appendSuccessFilesList(&b, review.Files, len(review.Files))
		}
		if path := strings.TrimSpace(review.PatchPath); path != "" {
			fmt.Fprintf(&b, "Patch file: %s\n", path)
		}
		if rpath := strings.TrimSpace(review.ReversePatchPath); rpath != "" {
			fmt.Fprintf(&b, "Undo patch file: %s\n", rpath)
		}
		b.WriteString("Use the transcript diff above and any diagnostics to inform your choice.\n\n")
	}

	appendSuccessFilesSection(&b, c.progress.SuccessFiles, "Files already accepted earlier this session (prefer new work unless necessary):\n", successFilesPromptLimit)
	if c.progress.AppliedPatches > 0 {
		fmt.Fprintf(&b, "Strict patch successes so far: %d. Accepting keeps them; rejecting reverts the latest patch only.\n\n", c.progress.AppliedPatches)
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

func (c *Client) appendForceRepatchExample(b *strings.Builder) {
	if !c.progress.ForceRepatchExample {
		return
	}
	b.WriteString("If the guard reports `Skipping patch because all target files were already updated earlier this session`, include `metadata.force_repatch: true` on the next patch. Example:\n")
	b.WriteString("Decision: patch\n")
	b.WriteString("{\n")
	b.WriteString("  \"metadata\": { \"description\": \"Reapply earlier edit\", \"force_repatch\": true },\n")
	b.WriteString("  \"edits\": [\n")
	b.WriteString("    { \"path\": \"docs/guide.md\", \"mode\": \"patch\", \"patch\": {\n")
	b.WriteString("      \"hunks\": [{\n")
	b.WriteString("        \"old_start\": 12, \"old_count\": 1, \"new_start\": 12, \"new_count\": 1,\n")
	b.WriteString("        \"context_before\": [\"## Overview\"],\n")
	b.WriteString("        \"deletions\": [\"The current text.\"],\n")
	b.WriteString("        \"additions\": [\"The corrected text.\"],\n")
	b.WriteString("        \"context_after\": [\"Use with caution.\"],\n")
	b.WriteString("        \"snippet_source\": { \"start_line\": 12, \"end_line\": 12 }\n")
	b.WriteString("      }]\n")
	b.WriteString("    } }\n")
	b.WriteString("  ]\n")
	b.WriteString("}\n\n")
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

const (
	maxDecisionPreambleChars = 600
	maxDecisionPreambleLines = 3
)

func parseDecision(resp string, patchEnabled bool) (Decision, string, string) {
	trimmed := strings.TrimSpace(resp)
	if trimmed == "" {
		return "", "", ""
	}

	lines := strings.Split(trimmed, "\n")
	if len(lines) == 0 {
		return "", "", ""
	}

	var (
		decisionIdx   = -1
		preambleLines []string
		preambleChars int
	)

	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "decision:") {
			decisionIdx = i
			break
		}

		preambleLines = append(preambleLines, line)
		preambleChars += len([]rune(line))
		if preambleChars > maxDecisionPreambleChars || len(preambleLines) > maxDecisionPreambleLines {
			return "", "", ""
		}
	}

	if decisionIdx == -1 {
		return "", "", ""
	}

	line := strings.TrimSpace(lines[decisionIdx])
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", ""
	}
	decisionStr := strings.TrimSpace(strings.ToLower(parts[1]))
	var decision Decision
	switch {
	case strings.Contains(decisionStr, string(DecisionAccept)):
		decision = DecisionAccept
	case strings.Contains(decisionStr, string(DecisionReject)):
		decision = DecisionReject
	case decisionStr == string(DecisionAsk) || decisionStr == "question" || decisionStr == "instruction" || decisionStr == "message":
		decision = DecisionAsk
	case decisionStr == string(DecisionPatch):
		decision = DecisionPatch
	case decisionStr == string(DecisionFinalize):
		decision = DecisionFinalize
	default:
		return "", "", ""
	}
	if !patchEnabled && decision == DecisionPatch {
		// Fallback to a generic ask when patches are disabled to keep the agent progressing.
		return DecisionAsk, "Question: Considering the current transcript, produce the single next high-signal repository-focused prompt for mct.", strings.Join(preambleLines, "\n")
	}
	remainder := strings.TrimSpace(strings.Join(lines[decisionIdx+1:], "\n"))
	return decision, remainder, strings.Join(preambleLines, "\n")
}

func reportTrajectoryError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[trajectory] emit error: %v\n", err)
}
