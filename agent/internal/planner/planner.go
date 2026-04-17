package planner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/parser"
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

type AskMode string

const (
	AskModeNoShell AskMode = "no-shell"
	AskModeShell   AskMode = "shell"
	AskModeBoth    AskMode = "both"
)

const (
	successFilesPromptLimit = 12
	progressMaxTrackedFiles = 100
	reviewDiffPreviewLimit  = 6000
)

const (
	askGuardMaxRetries = 1
	defaultAskFallback = "Considering the current transcript, produce the single next high-signal repository-focused prompt for mct."
)

const (
	defaultPlanPatchDisabledIntro = "Patch requests are disabled for this run. Choose Ask to gather information or Finalize when the goal is complete."
	defaultPlanPatchEnabledIntro  = "Patch requests are enabled. Choose Ask to gather information, Patch to change files, or Finalize when the goal is complete.\nOptional patch shorthand:\nPatch: <repo-relative filepath>\nExample: Patch: src/main.go"
	defaultPlanPatchRules         = "If patch, return only the JSON payload—no commentary or fences. The patch schema will be provided after you choose Decision: patch."
	defaultPlanPatchStrictRules   = defaultPlanPatchRules
)

type ClientConfig struct {
	Model             llm.ResolvedModel
	Extras            map[string]any
	Alias             string
	Verbose           bool
	DryRun            bool
	InternetAccess    bool
	RequestTimeoutSec int
	PatchEnabled      bool
	StrictPatchMode   bool
	PatchFull         bool
	RepoRoot          string
	SessionID         string
	PlannerOverlay    string
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

type cacheUsageTracker struct {
	maxCachedTokens int
}

func (t *cacheUsageTracker) Observe(_ llm.ResolvedModel, usage llm.CacheUsageInfo) {
	if usage.CachedTokens > t.maxCachedTokens {
		t.maxCachedTokens = usage.CachedTokens
	}
}

func (t *cacheUsageTracker) UpdateConversation(conv *conversation.Conversation) {
	if t == nil || conv == nil || t.maxCachedTokens <= 0 {
		return
	}
	anchorIndex := activeCacheAnchorIndex(conv)
	if anchorIndex < 0 {
		return
	}
	metadata := conv.Messages[anchorIndex].Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	current := anchorCachedTokens(metadata)
	if t.maxCachedTokens > current {
		metadata[llm.CacheAnchorCachedTokensMetadataKey] = t.maxCachedTokens
	}
	conv.Messages[anchorIndex].Metadata = metadata
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
	HasPlannerOverlay bool
	PlannerOverlay    string
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

type askTemplateData struct {
	AskRequest string
	Guardrail  string
}

type askMixedMonitorData struct {
	Ask string
}

type askMonitorData struct {
	Ask string
}

type askUserDirectedMonitorData struct {
	Ask            string
	InternetAccess bool
	CurrentGoal    string
}

type askUserDirectedPurifierData struct {
	Ask         string
	Reason      string
	CurrentGoal string
}

type askMixedMonitorResult struct {
	IsMixed bool   `json:"is_mixed"`
	Reason  string `json:"reason"`
	Rewrite string `json:"rewrite"`
}

type askUserDirectedMonitorResult struct {
	IsUserDirected bool   `json:"is_user_directed"`
	Reason         string `json:"reason"`
}

type askUserDirectedPurifierResult struct {
	ShouldSuspend    bool   `json:"should_suspend"`
	PurifiedQuestion string `json:"purified_question"`
	Context          string `json:"context"`
	Reason           string `json:"reason"`
}

type UserDirectedAskOutcome struct {
	ShouldSuspend bool
	OriginalAsk   string
	Question      string
	Context       string
	Reason        string
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
func (c *Client) Plan(ctx context.Context, conv *conversation.Conversation, goal string, transcript string, step, maxSteps int, patchPlan *PatchPlan) (Decision, string, error) {
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
	if !reviewMode && conv == nil {
		return "", "", errors.New("planner: conversation is required")
	}
	if reviewMode {
		prompt := c.reviewPrompt(goal, transcript, step, maxSteps)
		promptLog = strings.TrimSpace(prompt)
		messages = []llm.Message{messageWithEstimatedTokens("user", promptLog)}
	} else {
		messages = c.buildPlanMessages(conv, goal, step, maxSteps, patchPlan)
		promptLog = renderMessagesForLogging(messages)
	}
	c.logTokenEstimate(messages)
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
	var usageTracker *cacheUsageTracker
	if !reviewMode && conv != nil && cacheControlEnabled(c.cfg.Model) {
		usageTracker = &cacheUsageTracker{}
		chatCtx = llm.WithCacheUsageObserver(chatCtx, usageTracker.Observe)
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
	if usageTracker != nil {
		usageTracker.UpdateConversation(conv)
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
	if dec == "" && !reviewMode {
		formatPrompt := strings.TrimSpace(c.planFormatErrorTemplate())
		if formatPrompt != "" {
			retryMessages := append([]llm.Message(nil), messages...)
			retryMessages = append(retryMessages, messageWithEstimatedTokens("user", formatPrompt))
			retryPromptLog := renderMessagesForLogging(retryMessages)
			if hasWriter {
				payload := map[string]any{
					"event_version": 1,
					"model_alias":   c.cfg.Alias,
					"model_name":    c.cfg.Model.Model,
					"step":          step,
					"max_steps":     maxSteps,
					"format_retry":  true,
				}
				payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(retryPromptLog, w.ExcerptLen()), "prompt")
				if err := w.Emit(ctx, trajectory.Event{Kind: "planner.request", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}); err != nil {
					reportTrajectoryError(err)
				}
			}
			retryStart := time.Now()
			retryResp, retryErr := c.chatMessages(chatCtx, retryMessages)
			retryDuration := time.Since(retryStart)
			if retryErr == nil {
				dec, q, preamble = parseDecision(retryResp, c.cfg.PatchEnabled)
			}
			if hasWriter {
				payload := map[string]any{
					"event_version": 1,
					"model_alias":   c.cfg.Alias,
					"model_name":    c.cfg.Model.Model,
					"step":          step,
					"max_steps":     maxSteps,
					"duration_ms":   retryDuration.Milliseconds(),
					"parse_ok":      dec != "",
					"format_retry":  true,
				}
				payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(retryPromptLog, w.ExcerptLen()), "prompt")
				payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(retryResp, w.ExcerptLen()), "response")
				if dec != "" {
					payload["parse"] = map[string]any{"decision": string(dec)}
				}
				if preamble != "" {
					payload["ignored_preamble"] = trajectory.MakeTextExcerpt(preamble, w.ExcerptLen())
				}
				level := "info"
				var errInfo *trajectory.ErrorInfo
				if retryErr != nil {
					level = "error"
					category, code := llm.ClassifyError(retryErr)
					errInfo = &trajectory.ErrorInfo{Message: retryErr.Error(), Category: category, Code: code}
				} else if dec == "" {
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
	if dec == DecisionAsk {
		ask, err := c.generateAsk(ctx, conv, goal, transcript, step, maxSteps)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(ask) == "" {
			ask = defaultAskFallback
		}
		return DecisionAsk, ask, nil
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

func (c *Client) generateAsk(ctx context.Context, conv *conversation.Conversation, goal string, transcript string, step, maxSteps int) (string, error) {
	_ = transcript
	askRequest := c.buildAskRequest(conv, goal, step, maxSteps)
	guardrail := ""
	lastAsk := ""
	for attempt := 0; attempt <= askGuardMaxRetries; attempt++ {
		prompt := c.askPrompt(askRequest, guardrail)
		if strings.TrimSpace(prompt) == "" {
			return "", errors.New("planner: ask prompt template missing")
		}
		resp, err := c.chatPlannerTask(ctx, conv, goal, step, maxSteps, nil, prompt)
		if err != nil {
			return "", err
		}
		mode, ask, parseErr := parseAskMenu(resp)
		if parseErr != nil {
			guardrail = "The previous response did not follow the required ask format. Return exactly `Ask Mode:` and `Ask:` with no answer content. Legacy `both` output may be accepted, but it is treated as `shell`."
			continue
		}
		ask = strings.TrimSpace(ask)
		if ask == "" {
			guardrail = "The previous ask was empty. Provide a concrete ask."
			continue
		}
		if looksLikeAnswerInsteadOfAsk(ask) {
			guardrail = "The previous response answered the goal instead of proposing the next ask. Return only the next ask prompt in the required format."
			continue
		}
		lastAsk = ask
		if mode == AskModeNoShell {
			mixed, err := c.monitorAskMixedWithPlanner(ctx, conv, goal, ask, step, maxSteps)
			if err == nil && mixed.IsMixed {
				guardrail = buildAskMixedGuardrail(mixed.Reason, mixed.Rewrite)
				continue
			}
		}
		monitor, err := c.monitorAskWithPlanner(ctx, conv, goal, ask, step, maxSteps)
		if err != nil {
			return ask, nil
		}
		if !monitor.HasPatchIntent {
			return ask, nil
		}
		guardrail = buildAskGuardrail(monitor.Reason)
	}
	if strings.TrimSpace(lastAsk) != "" {
		return lastAsk, nil
	}
	return defaultAskFallback, nil
}

func (c *Client) monitorAsk(ctx context.Context, ask string) (askMonitorResult, error) {
	return c.monitorAskWithPlanner(ctx, nil, "", ask, 0, 0)
}

func (c *Client) monitorAskMixed(ctx context.Context, ask string) (askMixedMonitorResult, error) {
	return c.monitorAskMixedWithPlanner(ctx, nil, "", ask, 0, 0)
}

func (c *Client) AnalyzeUserDirectedAsk(ctx context.Context, conv *conversation.Conversation, goal, ask string, step, maxSteps int) (UserDirectedAskOutcome, error) {
	ask = strings.TrimSpace(ask)
	if ask == "" {
		return UserDirectedAskOutcome{}, nil
	}
	monitor, err := c.monitorAskUserDirectedWithPlanner(ctx, conv, goal, ask, step, maxSteps)
	if err != nil {
		return UserDirectedAskOutcome{}, err
	}
	if !monitor.IsUserDirected {
		return UserDirectedAskOutcome{}, nil
	}
	purified, err := c.purifyAskUserDirectedWithPlanner(ctx, conv, goal, ask, monitor.Reason, step, maxSteps)
	if err != nil {
		return UserDirectedAskOutcome{}, err
	}
	if !purified.ShouldSuspend || strings.TrimSpace(purified.PurifiedQuestion) == "" {
		return UserDirectedAskOutcome{}, nil
	}
	return UserDirectedAskOutcome{
		ShouldSuspend: true,
		OriginalAsk:   ask,
		Question:      strings.TrimSpace(purified.PurifiedQuestion),
		Context:       strings.TrimSpace(purified.Context),
		Reason:        strings.TrimSpace(purified.Reason),
	}, nil
}

func (c *Client) monitorAskWithPlanner(ctx context.Context, conv *conversation.Conversation, goal string, ask string, step, maxSteps int) (askMonitorResult, error) {
	prompt := c.askMonitorPrompt(ask)
	if strings.TrimSpace(prompt) == "" {
		return askMonitorResult{}, errors.New("planner: ask monitor template missing")
	}
	resp, err := c.chatPlannerTask(ctx, conv, goal, step, maxSteps, nil, prompt)
	if err != nil {
		return askMonitorResult{}, err
	}
	return parseAskMonitorResponse(resp)
}

func (c *Client) monitorAskMixedWithPlanner(ctx context.Context, conv *conversation.Conversation, goal string, ask string, step, maxSteps int) (askMixedMonitorResult, error) {
	prompt := c.askMixedMonitorPrompt(ask)
	if strings.TrimSpace(prompt) == "" {
		return askMixedMonitorResult{}, errors.New("planner: ask mixed monitor template missing")
	}
	resp, err := c.chatPlannerTask(ctx, conv, goal, step, maxSteps, nil, prompt)
	if err != nil {
		return askMixedMonitorResult{}, err
	}
	return parseAskMixedMonitorResponse(resp)
}

func (c *Client) monitorAskUserDirectedWithPlanner(ctx context.Context, conv *conversation.Conversation, goal string, ask string, step, maxSteps int) (askUserDirectedMonitorResult, error) {
	currentGoal := strings.TrimSpace(goal)
	if conv != nil {
		if cg := strings.TrimSpace(conv.CurrentGoal()); cg != "" {
			currentGoal = cg
		}
	}
	prompt := c.askUserDirectedMonitorPrompt(ask, currentGoal)
	if strings.TrimSpace(prompt) == "" {
		return askUserDirectedMonitorResult{}, errors.New("planner: ask user-directed monitor template missing")
	}
	return chatPlannerTaskJSONWithFormatRetry(c, ctx, conv, goal, step, maxSteps, prompt, parseAskUserDirectedMonitorResponse)
}

func (c *Client) purifyAskUserDirectedWithPlanner(ctx context.Context, conv *conversation.Conversation, goal, ask, reason string, step, maxSteps int) (askUserDirectedPurifierResult, error) {
	currentGoal := strings.TrimSpace(goal)
	if conv != nil {
		if cg := strings.TrimSpace(conv.CurrentGoal()); cg != "" {
			currentGoal = cg
		}
	}
	prompt := c.askUserDirectedPurifierPrompt(ask, reason, currentGoal)
	if strings.TrimSpace(prompt) == "" {
		return askUserDirectedPurifierResult{}, errors.New("planner: ask user-directed purifier template missing")
	}
	return chatPlannerTaskJSONWithFormatRetry(c, ctx, conv, goal, step, maxSteps, prompt, parseAskUserDirectedPurifierResponse)
}

func chatPlannerTaskJSONWithFormatRetry[T any](c *Client, ctx context.Context, conv *conversation.Conversation, goal string, step, maxSteps int, finalUserPrompt string, parse func(string) (T, error)) (T, error) {
	var zero T
	resp, err := c.chatPlannerTask(ctx, conv, goal, step, maxSteps, nil, finalUserPrompt)
	if err != nil {
		return zero, err
	}
	parsed, err := parse(resp)
	if err == nil {
		return parsed, nil
	}
	formatPrompt := strings.TrimSpace(userDirectedJSONFormatRetryPrompt())
	if formatPrompt == "" {
		return zero, err
	}
	retryMessages := c.buildPlannerTaskMessages(conv, goal, step, maxSteps, nil, finalUserPrompt)
	retryMessages = append(retryMessages, messageWithEstimatedTokens("user", formatPrompt))
	c.logTokenEstimate(retryMessages)
	chatCtx := ctx
	var usageTracker *cacheUsageTracker
	if conv != nil && cacheControlEnabled(c.cfg.Model) {
		usageTracker = &cacheUsageTracker{}
		chatCtx = llm.WithCacheUsageObserver(chatCtx, usageTracker.Observe)
	}
	retryResp, retryErr := c.chatMessages(chatCtx, retryMessages)
	if retryErr != nil {
		return zero, retryErr
	}
	if usageTracker != nil {
		usageTracker.UpdateConversation(conv)
	}
	return parse(retryResp)
}

func (c *Client) buildPlanMessages(conv *conversation.Conversation, goal string, step, maxSteps int, patchPlan *PatchPlan) []llm.Message {
	goalForPrompt := strings.TrimSpace(goal)
	if conv != nil {
		if current := strings.TrimSpace(conv.CurrentGoal()); current != "" {
			goalForPrompt = current
		}
	}
	finalUserPrompt := c.planPrompt(conv, goalForPrompt, "", step, maxSteps, patchPlan)
	return c.buildPlannerTaskMessages(conv, goalForPrompt, step, maxSteps, patchPlan, finalUserPrompt)
}

func (c *Client) buildFinalizeMessages(conv *conversation.Conversation, goal string) []llm.Message {
	goalForPrompt := strings.TrimSpace(goal)
	if conv != nil {
		if current := strings.TrimSpace(conv.CurrentGoal()); current != "" {
			goalForPrompt = current
		}
	}
	finalUserPrompt := c.finalizePrompt(goalForPrompt, "")
	return c.buildPlannerTaskMessages(conv, goalForPrompt, 0, 0, nil, finalUserPrompt)
}

func (c *Client) buildPlannerTaskMessages(conv *conversation.Conversation, goal string, step, maxSteps int, patchPlan *PatchPlan, finalUserPrompt string) []llm.Message {
	goalForPrompt := strings.TrimSpace(goal)
	if conv != nil {
		if current := strings.TrimSpace(conv.CurrentGoal()); current != "" {
			goalForPrompt = current
		}
	}
	systemPrompt := strings.TrimSpace(c.planSystemPrompt(conv, goalForPrompt, step, maxSteps, patchPlan))
	messages := conv.ToChatMessages(systemPrompt)
	finalMessage := messageWithEstimatedTokens("user", strings.TrimSpace(finalUserPrompt))
	messages = append(messages, finalMessage)
	return c.ensurePlanCacheAnchor(conv, systemPrompt, finalMessage, messages, step)
}

func (c *Client) chatPlannerTask(ctx context.Context, conv *conversation.Conversation, goal string, step, maxSteps int, patchPlan *PatchPlan, finalUserPrompt string) (string, error) {
	if conv == nil {
		return c.chat(ctx, finalUserPrompt)
	}
	messages := c.buildPlannerTaskMessages(conv, goal, step, maxSteps, patchPlan, finalUserPrompt)
	c.logTokenEstimate(messages)
	chatCtx := ctx
	var usageTracker *cacheUsageTracker
	if cacheControlEnabled(c.cfg.Model) {
		usageTracker = &cacheUsageTracker{}
		chatCtx = llm.WithCacheUsageObserver(chatCtx, usageTracker.Observe)
	}
	resp, err := c.chatMessages(chatCtx, messages)
	if err != nil {
		return "", err
	}
	if usageTracker != nil {
		usageTracker.UpdateConversation(conv)
	}
	return resp, nil
}

func (c *Client) ensurePlanCacheAnchor(conv *conversation.Conversation, systemPrompt string, stepMessage llm.Message, messages []llm.Message, step int) []llm.Message {
	if conv == nil {
		return messages
	}
	if !cacheControlEnabled(c.cfg.Model) {
		return messages
	}
	if estimatePlanTokens(messages) < c.cfg.Model.CacheTriggerThreshold {
		return messages
	}
	anchorIndex := activeCacheAnchorIndex(conv)
	if anchorIndex < 0 {
		initialAnchorIndex := cacheAnchorIndexForPlan(len(messages), c.cfg.Model.CacheLookbackOffset)
		insertIndex := cacheAnchorInsertIndex(initialAnchorIndex, messages, len(conv.Messages), systemPrompt != "")
		anchorTokens := estimatePlanTokens(messages)
		conv.InsertMessageAt(insertIndex, "user", llm.CacheAnchorMarkerText, newCacheAnchorMetadata(conv, step, anchorTokens))
		refreshed := conv.ToChatMessages(systemPrompt)
		refreshed = append(refreshed, stepMessage)
		return refreshed
	}
	if !shouldRotateCacheAnchor(c.cfg.Model, conv.Messages[anchorIndex].Metadata, messages) {
		return messages
	}
	markCacheAnchorRetired(conv, anchorIndex)
	insertIndex := cacheAnchorInsertIndex(len(messages)-1, messages, len(conv.Messages), systemPrompt != "")
	anchorTokens := estimatePlanTokens(messages)
	conv.InsertMessageAt(insertIndex, "user", llm.CacheAnchorMarkerText, newCacheAnchorMetadata(conv, step, anchorTokens))
	refreshed := conv.ToChatMessages(systemPrompt)
	refreshed = append(refreshed, stepMessage)
	return refreshed
}

func cacheControlEnabled(model llm.ResolvedModel) bool {
	return strings.TrimSpace(model.CacheKeyName) != "" && model.CacheTriggerThreshold > 0 && len(model.CacheControl) > 0
}

func activeCacheAnchorIndex(conv *conversation.Conversation) int {
	if conv == nil {
		return -1
	}
	for i := len(conv.Messages) - 1; i >= 0; i-- {
		msg := conv.Messages[i]
		if !isCacheAnchorMessage(msg.Metadata) {
			continue
		}
		if cacheAnchorRetired(msg.Metadata) {
			continue
		}
		return i
	}
	return -1
}

func isCacheAnchorMessage(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	val, ok := metadata["type"].(string)
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(val), "cache_anchor")
}

func cacheAnchorRetired(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	if raw, ok := metadata[llm.CacheAnchorRetiredMetadataKey]; ok {
		switch v := raw.(type) {
		case bool:
			return v
		case string:
			return strings.EqualFold(strings.TrimSpace(v), "true")
		case int:
			return v != 0
		case int64:
			return v != 0
		case float64:
			return v != 0
		}
	}
	return false
}

func shouldRotateCacheAnchor(model llm.ResolvedModel, anchorMetadata map[string]any, messages []llm.Message) bool {
	if model.CacheReanchorTokens <= 0 && model.CacheReanchorMessages <= 0 {
		return false
	}
	anchorIndex := cacheAnchorMessageIndex(messages)
	if anchorIndex < 0 {
		return false
	}
	if model.CacheReanchorTokens > 0 {
		tokensSince := estimatePlanTokens(messages[anchorIndex+1:])
		if tokensSince >= model.CacheReanchorTokens {
			if cacheReanchorMinSatisfied(anchorMetadata, model.CacheReanchorMinCachedTokens) {
				return true
			}
		}
	}
	if model.CacheReanchorMessages > 0 {
		messagesSince := len(messages) - anchorIndex - 1
		if messagesSince >= model.CacheReanchorMessages {
			if cacheReanchorMinSatisfied(anchorMetadata, model.CacheReanchorMinCachedTokens) {
				return true
			}
		}
	}
	return false
}

func cacheReanchorMinSatisfied(anchorMetadata map[string]any, minCachedTokens int) bool {
	if minCachedTokens <= 0 {
		return true
	}
	return anchorCachedTokens(anchorMetadata) >= minCachedTokens
}

func markCacheAnchorRetired(conv *conversation.Conversation, index int) {
	if conv == nil || index < 0 || index >= len(conv.Messages) {
		return
	}
	metadata := conv.Messages[index].Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[llm.CacheAnchorRetiredMetadataKey] = true
	conv.Messages[index].Metadata = metadata
}

func cacheAnchorMessageIndex(messages []llm.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Metadata == nil {
			continue
		}
		val, ok := messages[i].Metadata["type"].(string)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(val), "cache_anchor") && !cacheAnchorRetired(messages[i].Metadata) {
			return i
		}
	}
	return -1
}

func newCacheAnchorMetadata(conv *conversation.Conversation, step, anchorTokens int) map[string]any {
	seq := nextCacheAnchorSeq(conv)
	metadata := map[string]any{
		"type":                             "cache_anchor",
		llm.CacheAnchorSequenceMetadataKey: seq,
		llm.CacheAnchorTurnMetadataKey:     step,
		llm.CacheAnchorTokensMetadataKey:   anchorTokens,
	}
	return metadata
}

func anchorCachedTokens(metadata map[string]any) int {
	if metadata == nil {
		return 0
	}
	if raw, ok := metadata[llm.CacheAnchorCachedTokensMetadataKey]; ok {
		switch v := raw.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func nextCacheAnchorSeq(conv *conversation.Conversation) int {
	if conv == nil {
		return 1
	}
	maxSeq := 0
	count := 0
	for _, msg := range conv.Messages {
		if !isCacheAnchorMessage(msg.Metadata) {
			continue
		}
		count++
		if raw, ok := msg.Metadata[llm.CacheAnchorSequenceMetadataKey]; ok {
			switch v := raw.(type) {
			case int:
				if v > maxSeq {
					maxSeq = v
				}
			case int64:
				if int(v) > maxSeq {
					maxSeq = int(v)
				}
			case float64:
				if int(v) > maxSeq {
					maxSeq = int(v)
				}
			}
		}
	}
	if maxSeq > 0 {
		return maxSeq + 1
	}
	if count > 0 {
		return count + 1
	}
	return 1
}

func estimatePlanTokens(messages []llm.Message) int {
	total := 0
	for _, msg := range messages {
		if msg.Metadata != nil {
			if raw, ok := msg.Metadata["estimated_tokens"]; ok {
				switch val := raw.(type) {
				case int:
					total += val
					continue
				case int64:
					total += int(val)
					continue
				case float64:
					total += int(val)
					continue
				}
			}
		}
		total += llm.EstimateMessageTokens(msg)
	}
	return total
}

func cacheAnchorIndexForPlan(messageCount int, lookback int) int {
	if messageCount <= 0 {
		return 0
	}
	if lookback <= 0 {
		lookback = 1
	}
	anchorIndex := messageCount - lookback
	if anchorIndex < 0 {
		return 0
	}
	if anchorIndex >= messageCount {
		return messageCount - 1
	}
	return anchorIndex
}

func cacheAnchorInsertIndex(anchorIndex int, messages []llm.Message, convCount int, hasSystem bool) int {
	systemOffset := 0
	if hasSystem {
		systemOffset = 1
	}
	insertIndex := anchorIndex - systemOffset
	if insertIndex < 0 {
		insertIndex = 0
	}
	if insertIndex > convCount {
		insertIndex = convCount
	}
	if anchorIndex < 0 || anchorIndex >= len(messages) {
		return insertIndex
	}
	role := strings.ToLower(strings.TrimSpace(messages[anchorIndex].Role))
	if role == "user" {
		convEndIndex := systemOffset + convCount
		if anchorIndex < convEndIndex {
			insertIndex++
		}
	}
	if insertIndex < 0 {
		insertIndex = 0
	}
	if insertIndex > convCount {
		insertIndex = convCount
	}
	return insertIndex
}

func messageWithEstimatedTokens(role, content string) llm.Message {
	msg := llm.Message{Role: role, Content: content}
	if strings.TrimSpace(content) == "" {
		return msg
	}
	msg.Metadata = map[string]any{"estimated_tokens": llm.EstimateMessageTokens(msg)}
	return msg
}

func (c *Client) logTokenEstimate(messages []llm.Message) {
	if !c.cfg.Verbose {
		return
	}
	total := 0
	for _, msg := range messages {
		total += llm.EstimateMessageTokens(msg)
	}
	fmt.Fprintf(os.Stderr, "[planner] estimated prompt tokens: %d\n", total)
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

// Finalize composes the final answer using the structured conversation on the shared planner thread.
func (c *Client) Finalize(ctx context.Context, conv *conversation.Conversation, goal string) (string, error) {
	if c.cfg.DryRun {
		return "[dry-run] Final answer would be composed here based on accumulated evidence.", nil
	}
	if conv == nil {
		return "", errors.New("planner: conversation is required")
	}
	messages := c.buildFinalizeMessages(conv, goal)
	promptLog := renderMessagesForLogging(messages)
	c.logTokenEstimate(messages)
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
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(promptLog, w.ExcerptLen()), "prompt")
		if err := w.Emit(ctx, trajectory.Event{Kind: "planner.request", SpanID: span.ID, ParentSpanID: parentSpan, Payload: payload}); err != nil {
			reportTrajectoryError(err)
		}
		callCtx = trajectory.ContextWithParentSpan(ctx, span.ID)
	}
	var usageTracker *cacheUsageTracker
	if cacheControlEnabled(c.cfg.Model) {
		usageTracker = &cacheUsageTracker{}
		callCtx = llm.WithCacheUsageObserver(callCtx, usageTracker.Observe)
	}
	start := time.Now()
	resp, err := c.chatMessages(callCtx, messages)
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
	if usageTracker != nil {
		usageTracker.UpdateConversation(conv)
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
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(promptLog, w.ExcerptLen()), "prompt")
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

func (c *Client) planSystemPrompt(conv *conversation.Conversation, goal string, step, maxSteps int, patchPlan *PatchPlan) string {
	tpl := c.planSystemTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] plan system template missing")
		return ""
	}
	data := c.buildPlanTemplateData(conv, goal, "", step, maxSteps, patchPlan)
	rendered, err := prompts.Render("planner_plan_system_prompt", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] plan system template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) planSystemTemplate() string {
	if c.cfg.Prompts != nil {
		if trimmed := strings.TrimSpace(c.cfg.Prompts.PlanSystemPrompt); trimmed != "" {
			return trimmed
		}
	}
	if embedded, err := templates.GetEmbeddedTemplate("planner.plan_system"); err == nil {
		return embedded
	}
	return ""
}

func (c *Client) planPrompt(conv *conversation.Conversation, goal string, transcript string, step, maxSteps int, patchPlan *PatchPlan) string {
	tpl := c.planTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] plan prompt template missing")
		return ""
	}
	data := c.buildPlanTemplateData(conv, goal, transcript, step, maxSteps, patchPlan)
	rendered, err := prompts.Render("planner_plan_prompt", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] plan prompt template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) askPrompt(askRequest, guardrail string) string {
	tpl := c.askPromptTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] ask prompt template missing")
		return ""
	}
	data := askTemplateData{
		AskRequest: strings.TrimSpace(askRequest),
		Guardrail:  strings.TrimSpace(guardrail),
	}
	rendered, err := prompts.Render("planner_ask_prompt", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] ask prompt template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) askPromptTemplate() string {
	if c.cfg.Prompts != nil {
		if trimmed := strings.TrimSpace(c.cfg.Prompts.AskPrompt); trimmed != "" {
			return trimmed
		}
	}
	if embedded, err := templates.GetEmbeddedTemplate("planner.ask_prompt"); err == nil {
		return embedded
	}
	return ""
}

func (c *Client) planFormatErrorTemplate() string {
	if c.cfg.Prompts != nil {
		if trimmed := strings.TrimSpace(c.cfg.Prompts.FormatErrorTemplate); trimmed != "" {
			return trimmed
		}
	}
	if embedded, err := templates.GetEmbeddedTemplate("planner.format_error_template"); err == nil {
		return embedded
	}
	return ""
}

func userDirectedJSONFormatRetryPrompt() string {
	return "Your previous reply did not follow the required output format. Reply with exactly one valid JSON object that matches the requested schema. Start with '{' and end with '}'. Do not include markdown fences, commentary, or any surrounding text."
}

func (c *Client) askMixedMonitorPrompt(ask string) string {
	tpl := c.askMixedMonitorTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] ask mixed monitor template missing")
		return ""
	}
	data := askMixedMonitorData{Ask: strings.TrimSpace(ask)}
	rendered, err := prompts.Render("planner_ask_mixed_monitor", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] ask mixed monitor template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) askMixedMonitorTemplate() string {
	if embedded, err := templates.GetEmbeddedTemplate("planner.ask_mixed_monitor"); err == nil {
		return embedded
	}
	return ""
}

func (c *Client) askMonitorPrompt(ask string) string {
	tpl := c.askMonitorTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] ask monitor template missing")
		return ""
	}
	data := askMonitorData{Ask: strings.TrimSpace(ask)}
	rendered, err := prompts.Render("planner_ask_monitor", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] ask monitor template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) askUserDirectedMonitorPrompt(ask, currentGoal string) string {
	tpl := c.askUserDirectedMonitorTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] ask user-directed monitor template missing")
		return ""
	}
	data := askUserDirectedMonitorData{Ask: strings.TrimSpace(ask), InternetAccess: c.cfg.InternetAccess, CurrentGoal: strings.TrimSpace(currentGoal)}
	rendered, err := prompts.Render("planner_ask_user_directed_monitor", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] ask user-directed monitor template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) askUserDirectedMonitorTemplate() string {
	if embedded, err := templates.GetEmbeddedTemplate("planner.ask_user_directed_monitor"); err == nil {
		return embedded
	}
	return ""
}

func (c *Client) askUserDirectedPurifierPrompt(ask, reason, currentGoal string) string {
	tpl := c.askUserDirectedPurifierTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] ask user-directed purifier template missing")
		return ""
	}
	data := askUserDirectedPurifierData{Ask: strings.TrimSpace(ask), Reason: strings.TrimSpace(reason), CurrentGoal: strings.TrimSpace(currentGoal)}
	rendered, err := prompts.Render("planner_ask_user_directed_purifier", tpl, data, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[planner] ask user-directed purifier template error: %v\n", err)
		return ""
	}
	return rendered
}

func (c *Client) askUserDirectedPurifierTemplate() string {
	if embedded, err := templates.GetEmbeddedTemplate("planner.ask_user_directed_purifier"); err == nil {
		return embedded
	}
	return ""
}

func (c *Client) askMonitorTemplate() string {
	if embedded, err := templates.GetEmbeddedTemplate("planner.ask_monitor"); err == nil {
		return embedded
	}
	return ""
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

func (c *Client) buildPlanTemplateData(conv *conversation.Conversation, goal string, transcript string, step, maxSteps int, patchPlan *PatchPlan) planTemplateData {
	display, overflow := successFilesDisplay(c.progress.SuccessFiles, successFilesPromptLimit)
	goalTrim := strings.TrimSpace(goal)
	transcriptTrim := strings.TrimSpace(transcript)
	goalUpdate := ""
	if conv != nil {
		if update, ok := conv.LatestGoalUpdate(); ok {
			goalUpdate = update
		}
	}
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
	if overlay := strings.TrimSpace(c.cfg.PlannerOverlay); overlay != "" {
		data.HasPlannerOverlay = true
		data.PlannerOverlay = overlay
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

func (c *Client) buildAskRequest(conv *conversation.Conversation, goal string, step, maxSteps int) string {
	goalForPrompt := strings.TrimSpace(goal)
	if conv != nil {
		if current := strings.TrimSpace(conv.CurrentGoal()); current != "" {
			goalForPrompt = current
		}
	}
	goalUpdate := ""
	if conv != nil {
		if update, ok := conv.LatestGoalUpdate(); ok {
			goalUpdate = strings.TrimSpace(update)
		}
	}
	var b strings.Builder
	b.WriteString("Use the prior planner conversation for context. Produce the single next high-signal ask for mct.\n\n")
	if goalForPrompt != "" {
		fmt.Fprintf(&b, "Current Goal:\n%s\n\n", goalForPrompt)
	}
	if goalUpdate != "" {
		fmt.Fprintf(&b, "Latest Goal (takes precedence):\n%s\n\n", goalUpdate)
	}
	if step > 0 && maxSteps > 0 {
		fmt.Fprintf(&b, "Planner step: %d of %d.", step, maxSteps)
	}
	return strings.TrimSpace(b.String())
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
	_ = transcript
	if tpl := c.finalizeTemplate(); tpl != "" {
		data := c.buildFinalizeTemplateData(goal, "")
		rendered, err := prompts.Render("planner_finalize_prompt", tpl, data, nil)
		if err == nil {
			return rendered
		}
		fmt.Fprintf(os.Stderr, "[planner] finalize prompt template error: %v\n", err)
	}
	return c.finalizePromptFallback(goal, "")
}

func (c *Client) finalizePromptFallback(goal string, transcript string) string {
	_ = transcript
	var b strings.Builder
	b.WriteString("Write the final answer to the current goal using the conversation above as the source of truth.\n\n")
	if strings.TrimSpace(goal) != "" {
		b.WriteString("Goal:\n")
		b.WriteString(goal + "\n\n")
	}
	b.WriteString("Produce a clear, self-contained final response grounded in the prior turns. If any important gaps or uncertainty remain, call them out briefly.")
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
	_ = transcript
	goalTrim := strings.TrimSpace(goal)
	transcriptTrim := ""
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
		preambleLines = nil
		preambleChars = 0
		for i, raw := range lines {
			line := strings.TrimSpace(raw)
			if line == "" {
				continue
			}
			lower := strings.ToLower(line)
			switch {
			case strings.HasPrefix(lower, "finalize:"):
				parts := strings.SplitN(line, ":", 2)
				remainder := ""
				if len(parts) == 2 {
					remainder = strings.TrimSpace(parts[1])
				}
				tail := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
				if tail != "" {
					if remainder != "" {
						remainder = remainder + "\n" + tail
					} else {
						remainder = tail
					}
				}
				return DecisionFinalize, remainder, strings.Join(preambleLines, "\n")
			case strings.HasPrefix(lower, "ask:"),
				strings.HasPrefix(lower, "question:"),
				strings.HasPrefix(lower, "instruction:"),
				strings.HasPrefix(lower, "message:"):
				parts := strings.SplitN(line, ":", 2)
				remainder := ""
				if len(parts) == 2 {
					remainder = strings.TrimSpace(parts[1])
				}
				tail := strings.TrimSpace(strings.Join(lines[i+1:], "\n"))
				if tail != "" {
					if remainder != "" {
						remainder = remainder + "\n" + tail
					} else {
						remainder = tail
					}
				}
				return DecisionAsk, remainder, strings.Join(preambleLines, "\n")
			}
		}
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

type askMonitorResult struct {
	HasPatchIntent bool   `json:"has_patch_intent"`
	Reason         string `json:"reason"`
}

func parseAskMenu(resp string) (AskMode, string, error) {
	trimmed := strings.TrimSpace(resp)
	if trimmed == "" {
		return "", "", errors.New("empty ask response")
	}
	lines := strings.Split(trimmed, "\n")
	var (
		mode   AskMode
		ask    string
		askIdx = -1
	)
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if mode == "" {
			if val, ok := splitLabel(line, "ask mode:", "mode:"); ok {
				mode = normalizeAskMode(val)
				continue
			}
		}
		if askIdx == -1 {
			if val, ok := splitLabel(line, "ask:", "question:", "instruction:", "message:"); ok {
				askIdx = i
				ask = strings.TrimSpace(val)
			}
		}
	}
	if askIdx != -1 {
		tail := strings.TrimSpace(strings.Join(lines[askIdx+1:], "\n"))
		if tail != "" {
			if ask != "" {
				ask = strings.TrimSpace(ask + "\n" + tail)
			} else {
				ask = tail
			}
		}
	} else if mode == AskModeBoth {
		body := strings.TrimSpace(removeLinesWithPrefixes(trimmed, "ask mode:", "mode:"))
		noShell, shell, err := parseAskSplit(body)
		if err != nil {
			return mode, "", errors.New("missing split ask content")
		}
		ask = strings.TrimSpace(collapseAskSplit(noShell, shell))
	} else {
		return mode, "", errors.New("missing ask label")
	}
	if mode == "" {
		mode = AskModeNoShell
	}
	if mode == AskModeBoth {
		mode = AskModeShell
	}
	if strings.TrimSpace(ask) == "" {
		return mode, "", errors.New("empty ask content")
	}
	return mode, ask, nil
}

func looksLikeAnswerInsteadOfAsk(ask string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(ask))
	if trimmed == "" {
		return false
	}
	switch {
	case strings.HasPrefix(trimmed, "## answer"),
		strings.HasPrefix(trimmed, "answer:"),
		strings.HasPrefix(trimmed, "final answer"),
		strings.HasPrefix(trimmed, "== conclusion"):
		return true
	}
	return strings.Count(ask, "Confidence:") >= 2
}

func normalizeAskMode(raw string) AskMode {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	normalized = strings.ReplaceAll(normalized, "-", "")
	normalized = strings.ReplaceAll(normalized, " ", "")
	switch normalized {
	case "noshell", "content", "file":
		return AskModeNoShell
	case "shell", "command", "commands":
		return AskModeShell
	case "both", "mixed":
		return AskModeBoth
	default:
		return ""
	}
}

func parseAskSplit(resp string) (string, string, error) {
	trimmed := strings.TrimSpace(resp)
	if trimmed == "" {
		return "", "", errors.New("empty ask split response")
	}
	var noShell string
	var shell string
	for _, raw := range strings.Split(trimmed, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if noShell == "" {
			if val, ok := splitLabel(line, "no-shell:", "no shell:", "noshell:", "content:"); ok {
				noShell = strings.TrimSpace(val)
				continue
			}
		}
		if shell == "" {
			if val, ok := splitLabel(line, "shell:", "command:", "commands:"); ok {
				shell = strings.TrimSpace(val)
				continue
			}
		}
	}
	if noShell == "" || shell == "" {
		return "", "", errors.New("missing no-shell or shell ask")
	}
	return noShell, shell, nil
}

func formatAskSplit(noShell, shell string) string {
	return strings.TrimSpace(fmt.Sprintf("No-shell: %s\nShell: %s", strings.TrimSpace(noShell), strings.TrimSpace(shell)))
}

func collapseAskSplit(noShell, shell string) string {
	parts := make([]string, 0, 2)
	if trimmed := strings.TrimSpace(noShell); trimmed != "" {
		parts = append(parts, trimmed)
	}
	if trimmed := strings.TrimSpace(shell); trimmed != "" {
		parts = append(parts, trimmed)
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func parseAskMonitorResponse(resp string) (askMonitorResult, error) {
	jsonBytes, err := parser.ExtractJSONObjectPayload(resp)
	if err != nil {
		if strings.TrimSpace(resp) == "" {
			return askMonitorResult{}, errors.New("empty ask monitor response")
		}
		return askMonitorResult{}, err
	}
	var result askMonitorResult
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return askMonitorResult{}, err
	}
	result.Reason = strings.TrimSpace(result.Reason)
	return result, nil
}

func parseAskMixedMonitorResponse(resp string) (askMixedMonitorResult, error) {
	jsonBytes, err := parser.ExtractJSONObjectPayload(resp)
	if err != nil {
		if strings.TrimSpace(resp) == "" {
			return askMixedMonitorResult{}, errors.New("empty ask mixed monitor response")
		}
		return askMixedMonitorResult{}, err
	}
	var result askMixedMonitorResult
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return askMixedMonitorResult{}, err
	}
	result.Reason = strings.TrimSpace(result.Reason)
	result.Rewrite = strings.TrimSpace(result.Rewrite)
	return result, nil
}

func parseAskUserDirectedMonitorResponse(resp string) (askUserDirectedMonitorResult, error) {
	jsonBytes, err := parser.ExtractJSONObjectPayload(resp)
	if err != nil {
		if strings.TrimSpace(resp) == "" {
			return askUserDirectedMonitorResult{}, errors.New("empty ask user-directed monitor response")
		}
		return askUserDirectedMonitorResult{}, err
	}
	var result askUserDirectedMonitorResult
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return askUserDirectedMonitorResult{}, err
	}
	result.Reason = strings.TrimSpace(result.Reason)
	return result, nil
}

func parseAskUserDirectedPurifierResponse(resp string) (askUserDirectedPurifierResult, error) {
	jsonBytes, err := parser.ExtractJSONObjectPayload(resp)
	if err != nil {
		if strings.TrimSpace(resp) == "" {
			return askUserDirectedPurifierResult{}, errors.New("empty ask user-directed purifier response")
		}
		return askUserDirectedPurifierResult{}, err
	}
	var result askUserDirectedPurifierResult
	if err := json.Unmarshal(jsonBytes, &result); err != nil {
		return askUserDirectedPurifierResult{}, err
	}
	result.PurifiedQuestion = strings.TrimSpace(result.PurifiedQuestion)
	result.Context = strings.TrimSpace(result.Context)
	result.Reason = strings.TrimSpace(result.Reason)
	return result, nil
}

func buildAskGuardrail(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "The previous ask included patch intent. Ask is not for changing, updating, deleting, or patching files. Patch must be chosen separately, and you will have another chance to choose Patch after this ask."
	}
	return fmt.Sprintf("The previous ask included patch intent (%s). Ask is not for changing, updating, deleting, or patching files. Patch must be chosen separately, and you will have another chance to choose Patch after this ask.", trimmed)
}

func buildAskMixedGuardrail(reason, rewrite string) string {
	trimmed := strings.TrimSpace(reason)
	rewriteTrim := strings.TrimSpace(rewrite)
	if trimmed == "" {
		trimmed = "mixed no-shell and shell requests"
	}
	suggestedShell := ""
	if rewriteTrim != "" {
		if noShell, shell, err := parseAskSplit(rewriteTrim); err == nil {
			suggestedShell = collapseAskSplit(noShell, shell)
		}
	}
	if rewriteTrim == "" {
		return fmt.Sprintf("The previous ask was mixed (%s). Restate it as either a single no-shell ask or a single shell ask. If shell work is involved, use Ask Mode: shell and combine it into one ask.", trimmed)
	}
	if suggestedShell != "" {
		return fmt.Sprintf("The previous ask was mixed (%s). Restate it as either a single no-shell ask or a single shell ask. If shell work is involved, use Ask Mode: shell and combine it into one ask. Suggested shell ask:\n%s", trimmed, suggestedShell)
	}
	return fmt.Sprintf("The previous ask was mixed (%s). Restate it as either a single no-shell ask or a single shell ask. If shell work is involved, use Ask Mode: shell and combine it into one ask.", trimmed)
}

func splitLabel(line string, labels ...string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	for _, label := range labels {
		if strings.HasPrefix(lower, label) {
			return strings.TrimSpace(trimmed[len(label):]), true
		}
	}
	return "", false
}

func removeLinesWithPrefixes(text string, labels ...string) string {
	lines := strings.Split(text, "\n")
	filtered := make([]string, 0, len(lines))
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		lower := strings.ToLower(trimmed)
		matched := false
		for _, label := range labels {
			if strings.HasPrefix(lower, label) {
				matched = true
				break
			}
		}
		if !matched {
			filtered = append(filtered, raw)
		}
	}
	return strings.TrimSpace(strings.Join(filtered, "\n"))
}

func reportTrajectoryError(err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[trajectory] emit error: %v\n", err)
}
