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
	"github.com/tursomari/machtiani/agent/internal/prompts"
	"github.com/tursomari/machtiani/agent/internal/templates"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

type Decision string

const (
	DecisionAsk      Decision = "ask"
	DecisionFinalize Decision = "finalize"
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
)

const (
	askGuardMaxRetries = 1
	defaultAskFallback = "Considering the current transcript, produce the single next high-signal repository-focused prompt for mct."
)

type ClientConfig struct {
	Model             llm.ResolvedModel
	Extras            map[string]any
	Alias             string
	Verbose           bool
	DryRun            bool
	InternetAccess    bool
	RequestTimeoutSec int
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
	SuccessFiles []string
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
	SuccessFiles      []string
	SuccessOverflow   int
	AllowFinalize     bool
	HasTranscript     bool
	Transcript        string
	Step              int
	MaxSteps          int
	HasPlannerOverlay bool
	PlannerOverlay    string
}

type finalizeTemplateData struct {
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

type askUserDirectedMonitorData struct {
	Ask            string
	InternetAccess bool
}

type askUserDirectedPurifierData struct {
	Ask    string
	Reason string
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

func NewClient(cfg ClientConfig) *Client {
	if cfg.Extras == nil {
		cfg.Extras = map[string]any{}
	}
	return &Client{cfg: cfg}
}

// UpdateProgress refreshes planner-aware session progress (e.g. prior
// success files) so prompts can steer the model away from redundant work.
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
	c.progress = Progress{
		SuccessFiles: files,
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
func (c *Client) Plan(ctx context.Context, conv *conversation.Conversation, goal string, transcript string, step, maxSteps int) (Decision, string, error) {
	if c.cfg.DryRun {
		if step < maxSteps {
			return DecisionAsk, "From the transcript, ask mct for the next most informative repository-focused prompt.", nil
		}
		return DecisionFinalize, "", nil
	}
	if conv == nil {
		return "", "", errors.New("planner: conversation is required")
	}
	var (
		messages  []llm.Message
		promptLog string
	)
	messages = c.buildPlanMessages(ctx, conv, goal, step, maxSteps)
	promptLog = renderMessagesForLogging(messages)
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
	if conv != nil && cacheControlEnabled(c.cfg.Model) {
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
	dec, q, preamble := parseDecision(resp)
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
				dec, q, preamble = parseDecision(retryResp)
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
		resp, err := c.chatPlannerTask(ctx, conv, goal, step, maxSteps, prompt)
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
	prompt := c.askUserDirectedMonitorPrompt(ask)
	if strings.TrimSpace(prompt) == "" {
		return askMonitorResult{}, errors.New("planner: ask monitor template missing")
	}
	resp, err := c.chatPlannerTask(ctx, conv, goal, step, maxSteps, prompt)
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
	resp, err := c.chatPlannerTask(ctx, conv, goal, step, maxSteps, prompt)
	if err != nil {
		return askMixedMonitorResult{}, err
	}
	return parseAskMixedMonitorResponse(resp)
}

func (c *Client) monitorAskUserDirectedWithPlanner(ctx context.Context, conv *conversation.Conversation, goal string, ask string, step, maxSteps int) (askUserDirectedMonitorResult, error) {
	prompt := c.askUserDirectedMonitorPrompt(ask)
	if strings.TrimSpace(prompt) == "" {
		return askUserDirectedMonitorResult{}, errors.New("planner: ask user-directed monitor template missing")
	}
	return chatPlannerTaskJSONWithFormatRetry(c, ctx, conv, goal, step, maxSteps, prompt, parseAskUserDirectedMonitorResponse)
}

func (c *Client) purifyAskUserDirectedWithPlanner(ctx context.Context, conv *conversation.Conversation, goal, ask, reason string, step, maxSteps int) (askUserDirectedPurifierResult, error) {
	prompt := c.askUserDirectedPurifierPrompt(ask, reason)
	if strings.TrimSpace(prompt) == "" {
		return askUserDirectedPurifierResult{}, errors.New("planner: ask user-directed purifier template missing")
	}
	return chatPlannerTaskJSONWithFormatRetry(c, ctx, conv, goal, step, maxSteps, prompt, parseAskUserDirectedPurifierResponse)
}

func chatPlannerTaskJSONWithFormatRetry[T any](c *Client, ctx context.Context, conv *conversation.Conversation, goal string, step, maxSteps int, finalUserPrompt string, parse func(string) (T, error)) (T, error) {
	var zero T
	resp, err := c.chatPlannerTask(ctx, conv, goal, step, maxSteps, finalUserPrompt)
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
	retryMessages := c.buildPlannerTaskMessages(ctx, conv, goal, step, maxSteps, finalUserPrompt)
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

func (c *Client) buildPlanMessages(ctx context.Context, conv *conversation.Conversation, goal string, step, maxSteps int) []llm.Message {
	finalUserPrompt := c.planPrompt(conv, strings.TrimSpace(goal), "", step, maxSteps)
	return c.buildPlannerTaskMessages(ctx, conv, strings.TrimSpace(goal), step, maxSteps, finalUserPrompt)
}

func (c *Client) buildFinalizeMessages(ctx context.Context, conv *conversation.Conversation, goal string) []llm.Message {
	finalUserPrompt := c.finalizePrompt(strings.TrimSpace(goal), "")
	return c.buildPlannerTaskMessages(ctx, conv, strings.TrimSpace(goal), 0, 0, finalUserPrompt)
}

func (c *Client) buildPlannerTaskMessages(ctx context.Context, conv *conversation.Conversation, goal string, step, maxSteps int, finalUserPrompt string) []llm.Message {
	systemPrompt := strings.TrimSpace(c.planSystemPrompt(conv, strings.TrimSpace(goal), step, maxSteps))
	messages := conv.ToChatMessages(systemPrompt)
	finalMessage := messageWithEstimatedTokens("user", strings.TrimSpace(finalUserPrompt))
	messages = append(messages, finalMessage)
	return c.ensurePlanCacheAnchor(ctx, conv, systemPrompt, finalMessage, messages, step)
}

func (c *Client) chatPlannerTask(ctx context.Context, conv *conversation.Conversation, goal string, step, maxSteps int, finalUserPrompt string) (string, error) {
	if conv == nil {
		return c.chat(ctx, finalUserPrompt)
	}
	messages := c.buildPlannerTaskMessages(ctx, conv, goal, step, maxSteps, finalUserPrompt)
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

func (c *Client) ensurePlanCacheAnchor(ctx context.Context, conv *conversation.Conversation, systemPrompt string, stepMessage llm.Message, messages []llm.Message, step int) []llm.Message {
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
		stampInsertionPrefixHash(conv, refreshed)
		refreshed = append(refreshed, stepMessage)
		return refreshed
	}
	if !shouldRotateCacheAnchor(ctx, c.cfg.Model, conv.Messages[anchorIndex].Metadata, messages) {
		return messages
	}
	markCacheAnchorRetired(conv, anchorIndex)
	insertIndex := cacheAnchorInsertIndex(len(messages)-1, messages, len(conv.Messages), systemPrompt != "")
	anchorTokens := estimatePlanTokens(messages)
	conv.InsertMessageAt(insertIndex, "user", llm.CacheAnchorMarkerText, newCacheAnchorMetadata(conv, step, anchorTokens))
	refreshed := conv.ToChatMessages(systemPrompt)
	stampInsertionPrefixHash(conv, refreshed)
	refreshed = append(refreshed, stepMessage)
	return refreshed
}

func cacheControlEnabled(model llm.ResolvedModel) bool {
	return strings.TrimSpace(model.CacheKeyName) != "" && model.CacheTriggerThreshold > 0 && len(model.CacheControl) > 0
}

// stampInsertionPrefixHash computes the insertion-time cache prefix hash for the
// most recently inserted anchor and stores it in the anchor's metadata within conv.
func stampInsertionPrefixHash(conv *conversation.Conversation, messages []llm.Message) {
	if conv == nil || len(messages) == 0 {
		return
	}
	anchorIndex := cacheAnchorMessageIndex(messages)
	if anchorIndex < 0 {
		return
	}
	hash := llm.CachePrefixHash(llm.FormatMessagesForHashing(messages), anchorIndex)
	if hash == "" {
		return
	}
	// Find and update the anchor in conv.
	for i := len(conv.Messages) - 1; i >= 0; i-- {
		msg := conv.Messages[i]
		if !isCacheAnchorMessage(msg.Metadata) {
			continue
		}
		if cacheAnchorRetired(msg.Metadata) {
			continue
		}
		if msg.Metadata == nil {
			msg.Metadata = map[string]any{}
		}
		msg.Metadata[llm.CacheAnchorInsertionPrefixHashMetadataKey] = hash
		conv.Messages[i] = msg
		return
	}
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

func shouldRotateCacheAnchor(ctx context.Context, model llm.ResolvedModel, anchorMetadata map[string]any, messages []llm.Message) bool {
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
			// Rotation blocked: token threshold exceeded but min cached tokens not met.
			if model.CacheReanchorMinCachedTokens > 0 {
				llm.EmitCacheWarning(ctx, "rotation_blocked", map[string]any{
					"anchor_sequence":     anchorSeqFromMetadata(anchorMetadata),
					"tokens_since_anchor": tokensSince,
					"min_cached_tokens":   model.CacheReanchorMinCachedTokens,
					"anchor_cached":       anchorCachedTokens(anchorMetadata),
					"reanchor_threshold":  model.CacheReanchorTokens,
				})
			}
		}
	}
	if model.CacheReanchorMessages > 0 {
		messagesSince := len(messages) - anchorIndex - 1
		if messagesSince >= model.CacheReanchorMessages {
			if cacheReanchorMinSatisfied(anchorMetadata, model.CacheReanchorMinCachedTokens) {
				return true
			}
			// Rotation blocked: message threshold exceeded but min cached tokens not met.
			if model.CacheReanchorMinCachedTokens > 0 {
				llm.EmitCacheWarning(ctx, "rotation_blocked", map[string]any{
					"anchor_sequence":       anchorSeqFromMetadata(anchorMetadata),
					"messages_since_anchor": messagesSince,
					"min_cached_tokens":     model.CacheReanchorMinCachedTokens,
					"anchor_cached":         anchorCachedTokens(anchorMetadata),
					"reanchor_messages":     model.CacheReanchorMessages,
				})
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

func anchorSeqFromMetadata(metadata map[string]any) int {
	if metadata == nil {
		return 0
	}
	if raw, ok := metadata[llm.CacheAnchorSequenceMetadataKey]; ok {
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


// Finalize composes the final answer using the structured conversation on the shared planner thread.
func (c *Client) Finalize(ctx context.Context, conv *conversation.Conversation, goal string) (string, error) {
	if c.cfg.DryRun {
		return "[dry-run] Final answer would be composed here based on accumulated evidence.", nil
	}
	if conv == nil {
		return "", errors.New("planner: conversation is required")
	}
	messages := c.buildFinalizeMessages(ctx, conv, goal)
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

func (c *Client) planSystemPrompt(conv *conversation.Conversation, goal string, step, maxSteps int) string {
	tpl := c.planSystemTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] plan system template missing")
		return ""
	}
	data := c.buildPlanTemplateData(conv, goal, "", step, maxSteps)
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

func (c *Client) planPrompt(conv *conversation.Conversation, goal string, transcript string, step, maxSteps int) string {
	tpl := c.planTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] plan prompt template missing")
		return ""
	}
	data := c.buildPlanTemplateData(conv, goal, transcript, step, maxSteps)
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

func (c *Client) askUserDirectedMonitorPrompt(ask string) string {
	tpl := c.askUserDirectedMonitorTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] ask user-directed monitor template missing")
		return ""
	}
	data := askUserDirectedMonitorData{Ask: strings.TrimSpace(ask), InternetAccess: c.cfg.InternetAccess}
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

func (c *Client) askUserDirectedPurifierPrompt(ask, reason string) string {
	tpl := c.askUserDirectedPurifierTemplate()
	if tpl == "" {
		fmt.Fprintln(os.Stderr, "[planner] ask user-directed purifier template missing")
		return ""
	}
	data := askUserDirectedPurifierData{Ask: strings.TrimSpace(ask), Reason: strings.TrimSpace(reason)}
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

func (c *Client) buildPlanTemplateData(conv *conversation.Conversation, goal string, transcript string, step, maxSteps int) planTemplateData {
	display, overflow := successFilesDisplay(c.progress.SuccessFiles, successFilesPromptLimit)
	transcriptTrim := strings.TrimSpace(transcript)
	data := planTemplateData{
		SuccessFiles:    display,
		SuccessOverflow: overflow,
		HasTranscript:   transcriptTrim != "",
		Transcript:      transcriptTrim,
		Step:            step,
		MaxSteps:        maxSteps,
		AllowFinalize:   true,
	}
	if overlay := strings.TrimSpace(c.cfg.PlannerOverlay); overlay != "" {
		data.HasPlannerOverlay = true
		data.PlannerOverlay = overlay
	}
	return data
}

func (c *Client) buildAskRequest(conv *conversation.Conversation, goal string, step, maxSteps int) string {
	var b strings.Builder
	b.WriteString("Use the prior planner conversation for context. Produce the single next high-signal ask for mct.\n\n")
	if step > 0 && maxSteps > 0 {
		fmt.Fprintf(&b, "Planner step: %d of %d.", step, maxSteps)
	}
	return strings.TrimSpace(b.String())
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
	_ = goal
	_ = transcript
	var b strings.Builder
	b.WriteString("[answer_the_user] Reply to the user now based on the conversation so far.\n\n")
	b.WriteString("Answer for the user's current need. Do not make further work requests. Use relevant prior `work_result` messages when helpful. If the latest user turn calls for a narrow or conversational reply, answer naturally instead of re-summarizing the whole session. If the latest user turn asks for a summary or wrap-up, provide it. If important uncertainty remains, mention it briefly.")
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
	_ = goal
	_ = transcript
	transcriptTrim := ""
	return finalizeTemplateData{
		HasTranscript: transcriptTrim != "",
		Transcript:    transcriptTrim,
	}
}

func truncateMiddle(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + "…" + s[len(s)-half:]
}

func sanitizeForPrompt(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	const maxLen = 2000
	if len(s) > maxLen {
		s = s[:maxLen] + "..."
	}
	return s
}

const (
	maxDecisionPreambleChars = 600
	maxDecisionPreambleLines = 3
)

func parseDecision(resp string) (Decision, string, string) {
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
	case decisionWord == string(DecisionAsk) || decisionWord == "question" || decisionWord == "instruction" || decisionWord == "message":
		decision = DecisionAsk
	case decisionWord == string(DecisionFinalize):
		decision = DecisionFinalize
	default:
		return "", "", ""
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
