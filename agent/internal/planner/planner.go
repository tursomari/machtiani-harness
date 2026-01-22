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
	"github.com/tursomari/machtiani/agent/internal/prompts"
	"github.com/tursomari/machtiani/agent/internal/templates"
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
	reviewDiffPreviewLimit  = 6000
)

const (
	defaultPlanPatchDisabledIntro = "Patch requests are disabled for this run. Decide either to: (a) produce one single, high-signal repository-focused prompt, or (b) finalize if enough information is gathered."
	defaultPlanPatchEnabledIntro  = "Decide either to: (a) produce one single, high-signal repository-focused prompt, (b) request a patch, or (c) finalize if enough information is gathered.\nAlternatively, to request a patch with minimal syntax:\nPatch: <repo-relative filepath>\nExample: Patch: src/main.go"
	defaultPlanPatchRules         = "If patch, return only the JSON payload—no commentary or fences. The patch schema will be provided after you choose Decision: patch."
	defaultPlanPatchStrictRules   = defaultPlanPatchRules
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
	Prompts           *llm.PlannerPromptsConfig
}

type Client struct {
	cfg      ClientConfig
	chatFn   func(context.Context, []llm.Message) (string, error)
	progress Progress
}

type Progress struct {
	SuccessFiles        []string
	AppliedPatches      int
	ForceRepatchExample bool
	PendingReview       *PendingReview
}

type planTemplateData struct {
	PatchEnabled      bool
	StrictPatchMode   bool
	ForceRepatch      bool
	SuccessFiles      []string
	SuccessOverflow   int
	AppliedPatches    int
	HasPatchPlan      bool
	PatchPlanComplete bool
	AllowFinalize     bool
	HasGoal           bool
	Goal              string
	HasGoalUpdate     bool
	GoalUpdate        string
	HasTranscript     bool
	Transcript        string
	Step              int
	MaxSteps          int
	PatchIntro        string
	PatchRules        string
}

type reviewTemplateData struct {
	HasPending      bool
	Pending         reviewPendingData
	SuccessFiles    []string
	SuccessOverflow int
	AppliedPatches  int
	Step            int
	MaxSteps        int
}

type reviewPendingData struct {
	Description    string
	HasDescription bool
	Sequence       int
	Insertions     int
	Deletions      int
	HasDiffStats   bool
	HasFiles       bool
	Files          []string
	PatchPath      string
	UndoPatchPath  string
	DiffPreview    string
	HasDiffPreview bool
}

type finalizeTemplateData struct {
	HasGoal       bool
	Goal          string
	HasTranscript bool
	Transcript    string
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

func successFilesDisplay(files []string, limit int) ([]string, int) {
	if len(files) == 0 {
		return nil, 0
	}
	if limit <= 0 || limit > len(files) {
		limit = len(files)
	}
	items := append([]string(nil), files[:limit]...)
	overflow := 0
	if len(files) > limit {
		overflow = len(files) - limit
	}
	return items, overflow
}

// Plan decides the next action using only the transcript context.
func (c *Client) Plan(ctx context.Context, goal string, transcript string, step, maxSteps int, patchPlan *PatchPlan) (Decision, string, error) {
	if c.cfg.DryRun {
		if step < maxSteps {
			return DecisionAsk, "From the transcript, ask mct for the next most informative repository-focused prompt.", nil
		}
		return DecisionFinalize, "", nil
	}
	reviewMode := c.progress.PendingReview != nil
	var (
		messages  []llm.Message
		promptLog string
	)
	if reviewMode {
		prompt := c.reviewPrompt(goal, transcript, step, maxSteps)
		promptLog = strings.TrimSpace(prompt)
		messages = []llm.Message{{Role: "user", Content: promptLog}}
	} else {
		messages = c.buildPlanMessages(goal, transcript, step, maxSteps, patchPlan)
		promptLog = renderMessagesForLogging(messages)
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
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(promptLog, w.ExcerptLen()), "prompt")
		if err := w.Emit(ctx, trajectory.Event{Kind: "planner.request", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}); err != nil {
			reportTrajectoryError(err)
		}
		chatCtx = trajectory.ContextWithParentSpan(ctx, span.ID)
	}
	start := time.Now()
	resp, err := c.chatMessages(chatCtx, messages)
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
	autoAcceptReview := false
	if reviewMode {
		if dec == "" || (dec != DecisionAccept && dec != DecisionReject) {
			autoAcceptReview = true
			dec = DecisionAccept
			q = "Reason: auto-accepted by default review policy"
		}
	}
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
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(promptLog, w.ExcerptLen()), "prompt")
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(resp, w.ExcerptLen()), "response")
		if dec != "" {
			payload["parse"] = map[string]any{"decision": string(dec)}
		}
		if autoAcceptReview {
			payload["auto_accept_review"] = true
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
	if dec == DecisionPatch && strings.TrimSpace(q) != "" {
		trimmed := strings.TrimSpace(q)
		if !strings.HasPrefix(trimmed, "{") {
			// Shorthand patch flow: the model selected a target path, not a JSON payload.
			// Immediately route to strict patch generation with full schema instructions.
			c.cfg.StrictPatchMode = true
			strictPayload, err := c.runStrictPatchFlow(ctx, goal, transcript, step, maxSteps, trimmed)
			if err != nil {
				dec = DecisionAsk
				q = strictPatchFallbackQuestion(err)
			} else {
				q = strictPayload
			}
		}
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
		if path, err := patchlog.WritePrompt(promptLog, meta); err != nil {
			if c.cfg.Verbose {
				fmt.Fprintf(os.Stderr, "[planner] failed to write patch prompt log: %v\n", err)
			}
		} else if c.cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[planner] patch prompt logged to %s\n", path)
		}
	}
	return dec, q, nil
}

func (c *Client) buildPlanMessages(goal, transcript string, step, maxSteps int, patchPlan *PatchPlan) []llm.Message {
	systemPrompt := strings.TrimSpace(c.planSystemPrompt(goal, transcript, step, maxSteps, patchPlan))
	goalTrim := strings.TrimSpace(goal)
	goalUpdate, beforeUpdate, afterUpdate := splitTranscriptAtGoalUpdate(strings.TrimSpace(transcript))

	messages := make([]llm.Message, 0, 6)
	if systemPrompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: systemPrompt})
	}
	if goalTrim != "" {
		messages = append(messages, llm.Message{Role: "user", Content: goalTrim})
	}
	if beforeUpdate != "" {
		messages = append(messages, llm.Message{Role: "assistant", Content: beforeUpdate})
	}
	if goalUpdate != "" {
		messages = append(messages, llm.Message{Role: "user", Content: goalUpdate})
	}
	if afterUpdate != "" {
		messages = append(messages, llm.Message{Role: "assistant", Content: afterUpdate})
	}
	messages = append(messages, llm.Message{Role: "user", Content: fmt.Sprintf("Step %d of %d. Decide.", step, maxSteps)})
	return messages
}

func renderMessagesForLogging(messages []llm.Message) string {
	var b strings.Builder
	for _, msg := range messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "unknown"
		}
		b.WriteString(role)
		b.WriteString(":\n")
		b.WriteString(content)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
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

func (c *Client) chatMessages(ctx context.Context, messages []llm.Message) (string, error) {
	if len(messages) == 0 {
		return "", errors.New("planner: messages must not be empty")
	}
	hasContent := false
	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) != "" {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return "", errors.New("planner: messages must include content")
	}
	if c.chatFn != nil {
		return c.chatFn(ctx, messages)
	}
	callCtx := ctx
	var cancel context.CancelFunc
	if c.cfg.RequestTimeoutSec > 0 {
		callCtx, cancel = context.WithTimeout(ctx, time.Duration(c.cfg.RequestTimeoutSec)*time.Second)
		defer cancel()
	}
	callCtx = llm.WithStage(callCtx, "planner")
	return llm.ChatWithResolved(callCtx, c.cfg.Model, c.cfg.Extras, messages)
}

func (c *Client) chat(ctx context.Context, prompt string) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("planner: prompt must not be empty")
	}
	return c.chatMessages(ctx, []llm.Message{{Role: "user", Content: prompt}})
}

func (c *Client) planSystemPrompt(goal string, transcript string, step, maxSteps int, patchPlan *PatchPlan) string {
	tpl := c.planSystemTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] plan system template missing")
		return ""
	}
	data := c.buildPlanTemplateData(goal, transcript, step, maxSteps, patchPlan)
	rendered, err := prompts.Render("planner_plan_system_prompt", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] plan system template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) planSystemTemplate() string {
	if c.cfg.Prompts != nil {
		if trimmed := strings.TrimSpace(c.cfg.Prompts.PlanPrompt); trimmed != "" {
			return trimmed
		}
	}
	if embedded, err := templates.GetEmbeddedTemplate("planner.plan_system"); err == nil {
		return embedded
	}
	return ""
}

func (c *Client) planPrompt(goal string, transcript string, step, maxSteps int, patchPlan *PatchPlan) string {
	tpl := c.planTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] plan prompt template missing")
		return ""
	}
	data := c.buildPlanTemplateData(goal, transcript, step, maxSteps, patchPlan)
	rendered, err := prompts.Render("planner_plan_prompt", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] plan prompt template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) planTemplate() string {
	if c.cfg.Prompts != nil {
		if trimmed := strings.TrimSpace(c.cfg.Prompts.PlanPrompt); trimmed != "" {
			return trimmed
		}
	}
	if embedded, err := templates.GetEmbeddedTemplate("planner.plan_prompt"); err == nil {
		return embedded
	}
	return ""
}

func (c *Client) buildPlanTemplateData(goal string, transcript string, step, maxSteps int, patchPlan *PatchPlan) planTemplateData {
	display, overflow := successFilesDisplay(c.progress.SuccessFiles, successFilesPromptLimit)
	goalTrim := strings.TrimSpace(goal)
	transcriptTrim := strings.TrimSpace(transcript)

	goalUpdate, _ := extractGoalUpdateFromTranscript(transcriptTrim)
	hasGoalUpdate := strings.TrimSpace(goalUpdate) != ""
	applied := c.progress.AppliedPatches
	if applied < 0 {
		applied = 0
	}
	data := planTemplateData{
		PatchEnabled:    c.cfg.PatchEnabled,
		StrictPatchMode: c.cfg.StrictPatchMode,
		ForceRepatch:    c.progress.ForceRepatchExample,
		SuccessFiles:    display,
		SuccessOverflow: overflow,
		AppliedPatches:  applied,
		HasGoal:         goalTrim != "",
		Goal:            goalTrim,
		HasGoalUpdate:   hasGoalUpdate,
		GoalUpdate:      goalUpdate,
		HasTranscript:   transcriptTrim != "",
		Transcript:      transcriptTrim,
		Step:            step,
		MaxSteps:        maxSteps,
		PatchIntro:      c.planPatchIntroText(),
	}
	if c.cfg.PatchEnabled {
		data.HasPatchPlan = patchPlan != nil && len(patchPlan.Items) > 0
		data.PatchPlanComplete = patchPlan != nil && patchPlan.AllComplete()
		data.AllowFinalize = data.PatchPlanComplete
	} else {
		data.AllowFinalize = true
	}
	data.PatchRules = c.planPatchRulesText(c.cfg.StrictPatchMode)
	return data
}

func splitTranscriptAtGoalUpdate(transcript string) (string, string, string) {
	clean := stripTranscriptPreamble(strings.TrimSpace(transcript))
	if clean == "" {
		return "", "", ""
	}

	lines := strings.Split(clean, "\n")
	markerIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if isGoalUpdateHeader(lines[i]) {
			markerIdx = i
			break
		}
	}

	if markerIdx == -1 {
		return "", clean, ""
	}

	before := strings.TrimSpace(strings.Join(lines[:markerIdx], "\n"))
	afterLines := lines[markerIdx+1:]
	endIdx := len(afterLines)
	for i, line := range afterLines {
		if isSectionHeader(line) {
			endIdx = i
			break
		}
	}
	goalUpdate := strings.TrimSpace(strings.Join(afterLines[:endIdx], "\n"))
	remaining := strings.TrimSpace(strings.Join(afterLines[endIdx:], "\n"))

	return goalUpdate, before, remaining
}

// extractGoalUpdateFromTranscript returns the content of the last goal update
// marker and the transcript with that section removed.

func extractGoalUpdateFromTranscript(transcript string) (goalUpdate string, cleanTranscript string) {
	goalUpdate, beforeUpdate, _ := splitTranscriptAtGoalUpdate(transcript)
	if goalUpdate == "" {
		return "", strings.TrimSpace(transcript)
	}
	return goalUpdate, beforeUpdate
}

func stripTranscriptPreamble(transcript string) string {
	trimmed := strings.TrimSpace(transcript)
	if trimmed == "" {
		return ""
	}
	turnMarker := "\n== TURN"
	if idx := strings.Index(trimmed, turnMarker); idx != -1 {
		return strings.TrimSpace(trimmed[idx+1:])
	}
	return trimmed
}

func isGoalUpdateHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if !strings.HasSuffix(trimmed, "GOAL UPDATE") {
		return false
	}
	head := strings.TrimSpace(strings.TrimSuffix(trimmed, "GOAL UPDATE"))
	if head == "" {
		return false
	}
	for i := 0; i < len(head); i++ {
		if head[i] != '=' {
			return false
		}
	}
	return true
}

func isSectionHeader(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "=") {
		return false
	}
	idx := 0
	for idx < len(trimmed) && trimmed[idx] == '=' {
		idx++
	}
	return idx > 0 && idx < len(trimmed) && trimmed[idx] == ' '
}

func (c *Client) planPatchIntroText() string {
	if c.cfg.Prompts != nil {
		if c.cfg.PatchEnabled {
			if val := strings.TrimSpace(c.cfg.Prompts.PlanPatchEnabledIntro); val != "" {
				return val
			}
		} else {
			if val := strings.TrimSpace(c.cfg.Prompts.PlanPatchDisabledIntro); val != "" {
				return val
			}
		}
	}
	if c.cfg.PatchEnabled {
		return defaultPlanPatchEnabledIntro
	}
	return defaultPlanPatchDisabledIntro
}

func (c *Client) planPatchRulesText(strict bool) string {
	if c.cfg.Prompts != nil {
		if strict {
			if val := strings.TrimSpace(c.cfg.Prompts.PlanPatchStrictRules); val != "" {
				return val
			}
		} else {
			if val := strings.TrimSpace(c.cfg.Prompts.PlanPatchRules); val != "" {
				return val
			}
		}
	}
	if strict {
		return defaultPlanPatchStrictRules
	}
	return defaultPlanPatchRules
}

func (c *Client) reviewPrompt(goal string, transcript string, step, maxSteps int) string {
	if tpl := c.reviewTemplate(); tpl != "" {
		data := c.buildReviewTemplateData(step, maxSteps)
		rendered, err := prompts.Render("planner_review_prompt", tpl, data, nil)
		if err == nil {
			return rendered
		}
		fmt.Fprintf(os.Stderr, "[planner] review prompt template error: %v\n", err)
	}
	return c.reviewPromptFallback(goal, transcript, step, maxSteps)
}

func (c *Client) reviewPromptFallback(goal string, transcript string, step, maxSteps int) string {
	review := c.progress.PendingReview
	var b strings.Builder
	b.WriteString("A patch was just applied. Examine the diff to ensure the change was as intended and that you did not reduplicate or unnecessarily delete anything outside of your intention -- reject if you spot mistakes or risky alterations.\n")
	b.WriteString("Accept keeps the changes. Reject applies the undo patch to revert them.\n")
	b.WriteString("Begin your reply immediately with `Decision:`—no leading commentary.\n")
	b.WriteString("Allowed values: accept or reject (case-insensitive).\n")
	b.WriteString("Always include a second line formatted `Reason: <brief justification>` that cites why the changes are correct and safe (even if you accept).\n\n")
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
			if diffPreview, err := loadPatchDiffPreview(path, reviewDiffPreviewLimit); err == nil && strings.TrimSpace(diffPreview) != "" {
				b.WriteString("\nPatch diff preview:\n")
				b.WriteString("```diff\n")
				b.WriteString(diffPreview)
				b.WriteString("\n```\n")
			}
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
	b.WriteString(fmt.Sprintf("Step %d of %d. Decide.\n", step, maxSteps))
	return b.String()
}

func (c *Client) reviewTemplate() string {
	if c.cfg.Prompts != nil {
		if trimmed := strings.TrimSpace(c.cfg.Prompts.ReviewPrompt); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (c *Client) buildReviewTemplateData(step, maxSteps int) reviewTemplateData {
	display, overflow := successFilesDisplay(c.progress.SuccessFiles, successFilesPromptLimit)
	data := reviewTemplateData{
		SuccessFiles:    display,
		SuccessOverflow: overflow,
		Step:            step,
		MaxSteps:        maxSteps,
	}
	applied := c.progress.AppliedPatches
	if applied > 0 {
		data.AppliedPatches = applied
	}
	review := c.progress.PendingReview
	if review == nil {
		return data
	}
	data.HasPending = true
	files := append([]string(nil), review.Files...)
	patchPath := strings.TrimSpace(review.PatchPath)
	undoPath := strings.TrimSpace(review.ReversePatchPath)
	desc := strings.TrimSpace(sanitizeForPrompt(review.Description))
	pending := reviewPendingData{
		Description:    desc,
		HasDescription: desc != "",
		Sequence:       review.Sequence,
		Insertions:     review.Insertions,
		Deletions:      review.Deletions,
		HasDiffStats:   review.Insertions != 0 || review.Deletions != 0,
		Files:          files,
		HasFiles:       len(files) > 0,
		PatchPath:      patchPath,
		UndoPatchPath:  undoPath,
	}
	if strings.TrimSpace(patchPath) != "" {
		if preview, err := loadPatchDiffPreview(patchPath, reviewDiffPreviewLimit); err == nil {
			trimmed := strings.TrimSpace(preview)
			if trimmed != "" {
				pending.DiffPreview = trimmed
				pending.HasDiffPreview = true
			}
		} else {
			fmt.Fprintf(os.Stderr, "[planner] diff preview error: %v\n", err)
		}
	}
	data.Pending = pending
	return data
}

func (c *Client) appendForceRepatchExample(b *strings.Builder) {
	if !c.progress.ForceRepatchExample {
		return
	}
	b.WriteString("If the guard reports `Skipping patch because all target files were already updated earlier this session`, include `metadata.force_repatch: true` on the next patch.\n\n")
}

func loadPatchDiffPreview(path string, limit int) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	data, err := os.ReadFile(trimmed)
	if err != nil {
		return "", err
	}
	diff := strings.TrimSpace(string(data))
	if diff == "" {
		return "", nil
	}
	if limit > 0 && len(diff) > limit {
		if limit > 3 {
			diff = diff[:limit-3] + "..."
		} else {
			diff = diff[:limit]
		}
		diff += "\n\n[diff truncated]"
	}
	return diff, nil
}

func (c *Client) finalizePrompt(goal string, transcript string) string {
	if tpl := c.finalizeTemplate(); tpl != "" {
		data := c.buildFinalizeTemplateData(goal, transcript)
		rendered, err := prompts.Render("planner_finalize_prompt", tpl, data, nil)
		if err == nil {
			return rendered
		}
		fmt.Fprintf(os.Stderr, "[planner] finalize prompt template error: %v\n", err)
	}
	return c.finalizePromptFallback(goal, transcript)
}

func (c *Client) finalizePromptFallback(goal string, transcript string) string {
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

func (c *Client) finalizeTemplate() string {
	if c.cfg.Prompts != nil {
		if trimmed := strings.TrimSpace(c.cfg.Prompts.FinalizePrompt); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (c *Client) buildFinalizeTemplateData(goal, transcript string) finalizeTemplateData {
	goalTrim := strings.TrimSpace(goal)
	transcriptTrim := strings.TrimSpace(transcript)
	return finalizeTemplateData{
		HasGoal:       goalTrim != "",
		Goal:          goalTrim,
		HasTranscript: transcriptTrim != "",
		Transcript:    transcriptTrim,
	}
}

type patchPlanGenerateData struct {
	Goal       string
	Transcript string
}

type patchPlanUpdateData struct {
	Goal             string
	ExistingPlanJSON string
	RecentTranscript string
	LastPatchedFile  string
}

// GeneratePatchPlan asks the LLM to propose a plan of file edits based on the goal and transcript.
// Returns nil when the response cannot be parsed so patch flow can continue without a plan.
func (c *Client) GeneratePatchPlan(ctx context.Context, goal, transcript string) (*PatchPlan, error) {
	prompt := c.renderPatchPlanGeneratePrompt(goal, transcript)
	if prompt == "" {
		return nil, nil
	}
	plan, err := c.requestPatchPlan(ctx, prompt)
	if err != nil {
		return nil, err
	}
	if plan != nil {
		return plan, nil
	}
	fallback := c.simplifiedPatchPlanGeneratePrompt(goal, transcript)
	if fallback == "" || fallback == prompt {
		return nil, nil
	}
	return c.requestPatchPlan(ctx, fallback)
}

// UpdatePatchPlan refreshes an existing plan, marking items complete when appropriate and adding new tasks.
// Returns nil when the response cannot be parsed so patch flow can continue without a plan.
func (c *Client) UpdatePatchPlan(ctx context.Context, goal, transcript string, existing *PatchPlan, lastPatchedFile string) (*PatchPlan, error) {
	dataJSON := ""
	if existing != nil {
		if encoded, err := json.MarshalIndent(existing, "", "  "); err == nil {
			dataJSON = string(encoded)
		} else {
			fmt.Fprintf(os.Stderr, "[planner] failed to marshal existing patch plan: %v\n", err)
		}
	}
	prompt := c.renderPatchPlanUpdatePrompt(goal, transcript, dataJSON, lastPatchedFile)
	if prompt == "" {
		return nil, nil
	}
	plan, err := c.requestPatchPlan(ctx, prompt)
	if err != nil {
		return nil, err
	}
	if plan != nil {
		return plan, nil
	}
	fallback := c.simplifiedPatchPlanUpdatePrompt(goal, transcript, dataJSON, lastPatchedFile)
	if fallback == "" || fallback == prompt {
		return nil, nil
	}
	return c.requestPatchPlan(ctx, fallback)
}

func (c *Client) renderPatchPlanGeneratePrompt(goal, transcript string) string {
	tpl, err := templates.GetEmbeddedTemplate("planner.patch_plan_generate")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] patch plan generate template missing: %v\n", err)
		return ""
	}
	data := patchPlanGenerateData{Goal: strings.TrimSpace(goal), Transcript: strings.TrimSpace(transcript)}
	rendered, err := prompts.Render("planner_patch_plan_generate", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] patch plan generate template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) renderPatchPlanUpdatePrompt(goal, transcript, existingPlanJSON, lastPatchedFile string) string {
	tpl, err := templates.GetEmbeddedTemplate("planner.patch_plan_update")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] patch plan update template missing: %v\n", err)
		return ""
	}
	data := patchPlanUpdateData{
		Goal:             strings.TrimSpace(goal),
		ExistingPlanJSON: strings.TrimSpace(existingPlanJSON),
		RecentTranscript: strings.TrimSpace(transcript),
		LastPatchedFile:  strings.TrimSpace(lastPatchedFile),
	}
	rendered, err := prompts.Render("planner_patch_plan_update", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] patch plan update template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) simplifiedPatchPlanGeneratePrompt(goal, transcript string) string {
	goal = strings.TrimSpace(goal)
	transcript = strings.TrimSpace(transcript)
	if goal == "" && transcript == "" {
		return ""
	}
	return fmt.Sprintf("Goal: %s\nTranscript:\n%s\nReturn only JSON patch plan with a goal summary and items describing file edits in format: {\"goal\": \"<intro statement>\", \"items\": [...]}.", goal, transcript)
}

func (c *Client) simplifiedPatchPlanUpdatePrompt(goal, transcript, existingPlanJSON, lastPatchedFile string) string {
	goal = strings.TrimSpace(goal)
	transcript = strings.TrimSpace(transcript)
	existingPlanJSON = strings.TrimSpace(existingPlanJSON)
	lastPatchedFile = strings.TrimSpace(lastPatchedFile)
	if goal == "" && transcript == "" && existingPlanJSON == "" {
		return ""
	}
	var b strings.Builder
	if goal != "" {
		fmt.Fprintf(&b, "Goal: %s\n", goal)
	}
	if lastPatchedFile != "" {
		fmt.Fprintf(&b, "Patched file: %s\n", lastPatchedFile)
	}
	if existingPlanJSON != "" {
		b.WriteString("Current plan:\n")
		b.WriteString(existingPlanJSON)
		b.WriteString("\n")
	}
	if transcript != "" {
		b.WriteString("Recent transcript:\n")
		b.WriteString(transcript)
		b.WriteString("\n")
	}
	b.WriteString("Return updated JSON patch plan with the goal preserved or refined and completed items marked in format: {\"goal\": \"<intro statement>\", \"items\": [...]}.")
	return b.String()
}

func (c *Client) requestPatchPlan(ctx context.Context, prompt string) (*PatchPlan, error) {
	resp, err := c.chat(ctx, prompt)
	if err != nil {
		return nil, err
	}
	plan, err := parsePatchPlan(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] patch plan parse failed: %v\n", err)
		return nil, nil
	}
	return plan, nil
}

func parsePatchPlan(raw string) (*PatchPlan, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("empty patch plan response")
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start != -1 && end != -1 && start < end {
		trimmed = strings.TrimSpace(trimmed[start : end+1])
	}
	var plan PatchPlan
	if err := json.Unmarshal([]byte(trimmed), &plan); err != nil {
		return nil, err
	}
	return &plan, nil
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
	if patchEnabled {
		trimmedLower := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmedLower, "patch:") {
			path := strings.TrimSpace(trimmed[len("Patch:"):])
			if path == "" {
				return "", "", ""
			}
			// Shorthand patch requests specify the file path only.
			// The planner will route to patch-generation with full schema instructions.
			return DecisionPatch, path, ""
		}
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
	decisionTail := strings.TrimSpace(parts[1])
	decisionStr := strings.TrimSpace(strings.ToLower(decisionTail))
	decisionWord := decisionStr
	if decisionWord != "" {
		fields := strings.FieldsFunc(decisionWord, func(r rune) bool {
			return r == ':' || r == ' ' || r == '\t'
		})
		if len(fields) > 0 {
			decisionWord = fields[0]
		}
	}
	var decision Decision
	switch {
	case strings.Contains(decisionStr, string(DecisionAccept)):
		decision = DecisionAccept
	case strings.Contains(decisionStr, string(DecisionReject)):
		decision = DecisionReject
	case decisionWord == string(DecisionAsk) || decisionWord == "question" || decisionWord == "instruction" || decisionWord == "message":
		decision = DecisionAsk
	case decisionWord == string(DecisionPatch):
		decision = DecisionPatch
	case decisionWord == string(DecisionFinalize):
		decision = DecisionFinalize
	default:
		return "", "", ""
	}
	if !patchEnabled && decision == DecisionPatch {
		// Fallback to a generic ask when patches are disabled to keep the agent progressing.
		return DecisionAsk, "Question: Considering the current transcript, produce the single next high-signal repository-focused prompt for mct.", strings.Join(preambleLines, "\n")
	}
	remainder := strings.TrimSpace(strings.Join(lines[decisionIdx+1:], "\n"))
	if decision == DecisionAsk && remainder == "" && decisionTail != "" && strings.ToLower(decisionTail) != decisionWord {
		remainder = decisionTail
	}
	return decision, remainder, strings.Join(preambleLines, "\n")
}

func reportTrajectoryError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[trajectory] emit error: %v\n", err)
}
