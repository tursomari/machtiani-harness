package discovery

import (
	"context"
	"fmt"
	"sort"
	"strings"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

const (
	discoveryStateSummaryPrefix = "Discovery state summary (compacted older exchanges):"
	discoveryCompactionMarker   = "[TRUNCATED: older tool output omitted to fit the active discovery budget]"
	discoveryCueMarker          = "[TRUNCATED: older discovery cue material omitted]"
)

type discoveryState struct {
	seenPaths      map[string]struct{}
	failedPatterns []string
	recentCommands []string
}

type discoveryFitReport struct {
	Compacted          bool
	ToolOutputsTrimmed int
	InitialCueTrimmed  bool
	EstimatedTokens    int
}

type discoveryRequestPolicy struct {
	active llm.InputBudget
}

var persistLearnedContext = llm.PersistLearnedContext

func (p *discoveryRequestPolicy) call(
	ctx context.Context,
	cfg cfgpkg.Config,
	llmCfg LLMSettings,
	messages []chatMessage,
	initialCore string,
	state discoveryState,
	tr *cfgpkg.TrajectoryRecorder,
	round int,
	details map[string]any,
) (string, []chatMessage, error) {
	starting := p.active
	for reduction := 0; ; reduction++ {
		fitted, report, err := fitDiscoveryMessages(messages, initialCore, state, p.active.MaxInputTokens)
		if err != nil {
			return "", nil, err
		}
		if tr != nil && tr.Enabled {
			payload := map[string]any{
				"messages":             fitted,
				"estimated_tokens":     report.EstimatedTokens,
				"max_input_tokens":     p.active.MaxInputTokens,
				"compacted":            report.Compacted,
				"tool_outputs_trimmed": report.ToolOutputsTrimmed,
				"initial_cue_trimmed":  report.InitialCueTrimmed,
				"overflow_reduction":   reduction,
			}
			for key, value := range details {
				payload[key] = value
			}
			tr.Event("llm_request", round, payload)
		}

		llmCtx, cancel := llmCallContext(ctx, cfg.LLMTimeoutSec)
		content, callErr := chatInvoker(llmCtx, llmCfg, fitted)
		cancel()
		if callErr == nil {
			if reduction > 0 {
				persisted := false
				var persistErr error
				if llmCfg.ContextIdentityUnambiguous {
					persisted, persistErr = persistLearnedContext(llmCfg.Model, starting.ContextLength, p.active.ContextLength)
				}
				llm.EmitContextAdjustment(ctx, llmCfg.Model, starting.ContextLength, p.active.ContextLength, persisted, persistErr)
			}
			return content, fitted, nil
		}
		if !llm.IsContextOverflow(callErr) || p.active.MaxInputTokens <= 0 || reduction >= 4 {
			return "", fitted, callErr
		}

		nextInput := p.active.MaxInputTokens / 2
		nextLength := llm.ContextLengthForInputCap(nextInput)
		next, budgetErr := llm.BudgetForContextLength(nextLength, llm.ContextSourceRuntimeLearned)
		if budgetErr != nil {
			return "", fitted, budgetErr
		}
		if next.MaxInputTokens >= p.active.MaxInputTokens {
			return "", fitted, callErr
		}
		p.active = next
		llm.EmitContextBudget(ctx, llmCfg.Model, p.active)
	}
}

func cloneChatMessages(messages []chatMessage) []chatMessage {
	return append([]chatMessage(nil), messages...)
}

func estimateChatMessages(messages []chatMessage) int {
	converted := make([]llm.Message, len(messages))
	for i, message := range messages {
		converted[i] = llm.Message{Role: message.Role, Content: message.Content}
	}
	return llm.EstimateMessagesTokens(converted)
}

func buildDiscoveryStateSummary(state discoveryState, maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	paths := make([]string, 0, len(state.seenPaths))
	for path := range state.seenPaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	if len(paths) > 40 {
		paths = paths[len(paths)-40:]
	}
	failed := append([]string(nil), state.failedPatterns...)
	if len(failed) > 12 {
		failed = failed[len(failed)-12:]
	}
	commands := append([]string(nil), state.recentCommands...)
	if len(commands) > 12 {
		commands = commands[len(commands)-12:]
	}
	if len(paths) == 0 && len(failed) == 0 && len(commands) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(discoveryStateSummaryPrefix)
	if len(paths) > 0 {
		b.WriteString("\nUseful paths already observed:")
		for _, path := range paths {
			fmt.Fprintf(&b, "\n- %s", path)
		}
	}
	if len(failed) > 0 {
		b.WriteString("\nFile-search patterns that returned no results:")
		for _, pattern := range failed {
			fmt.Fprintf(&b, "\n- %s", pattern)
		}
	}
	if len(commands) > 0 {
		b.WriteString("\nRecent discovery commands:")
		for _, command := range commands {
			fmt.Fprintf(&b, "\n- %s", command)
		}
	}

	summary, _, err := llm.TruncateLinesKeepTail(b.String(), maxTokens, "[TRUNCATED: older discovery state omitted]")
	if err != nil {
		return ""
	}
	return summary
}

func lastTwoExchangeStart(messages []chatMessage) int {
	assistantIndices := make([]int, 0, 2)
	for i := len(messages) - 1; i >= 2; i-- {
		if messages[i].Role == "assistant" {
			assistantIndices = append(assistantIndices, i)
			if len(assistantIndices) == 2 {
				return assistantIndices[1]
			}
		}
	}
	if len(assistantIndices) == 1 {
		return assistantIndices[0]
	}
	return len(messages)
}

func isToolOutput(content string) bool {
	return strings.Contains(content, "RG_OUT:") || strings.Contains(content, "SED_OUT[") || strings.Contains(content, "LS_OUT[")
}

func fitMessageContent(messages []chatMessage, index, maxTotal int, marker string) (bool, error) {
	original := messages[index].Content
	if estimateChatMessages(messages) <= maxTotal {
		return false, nil
	}
	messages[index].Content = ""
	available := maxTotal - estimateChatMessages(messages)
	messages[index].Content = original
	if available <= 0 {
		messages[index].Content = ""
		return strings.TrimSpace(original) != "", nil
	}
	fitted, truncated, err := llm.TruncateLinesKeepTail(original, available, marker)
	if err != nil {
		return false, err
	}
	messages[index].Content = fitted
	return truncated, nil
}

// fitDiscoveryMessages enforces the token cap before every request. It keeps
// the protocol core and the newest two assistant/tool exchanges, replaces
// older completed exchanges with bounded state, trims retained tool output,
// and only then reduces the dynamic portion of the initial cue.
func fitDiscoveryMessages(messages []chatMessage, initialCore string, state discoveryState, maxTokens int) ([]chatMessage, discoveryFitReport, error) {
	fitted := cloneChatMessages(messages)
	report := discoveryFitReport{EstimatedTokens: estimateChatMessages(fitted)}
	if maxTokens <= 0 || report.EstimatedTokens <= maxTokens {
		return fitted, report, nil
	}
	if len(fitted) < 2 || fitted[0].Role != "system" || !strings.HasPrefix(fitted[1].Content, initialCore) {
		return nil, report, fmt.Errorf("discovery history is missing its fixed protocol core")
	}

	keepStart := lastTwoExchangeStart(fitted)
	if keepStart > 2 {
		retained := make([]chatMessage, 0, 3+len(fitted)-keepStart)
		retained = append(retained, fitted[:2]...)
		summaryLimit := max(64, min(512, maxTokens/8))
		if summary := buildDiscoveryStateSummary(state, summaryLimit); summary != "" {
			retained = append(retained, chatMessage{Role: "user", Content: summary})
		}
		retained = append(retained, fitted[keepStart:]...)
		fitted = retained
		report.Compacted = true
	}

	for i := 2; i < len(fitted) && estimateChatMessages(fitted) > maxTokens; i++ {
		if fitted[i].Role != "user" || !isToolOutput(fitted[i].Content) {
			continue
		}
		trimmed, err := fitMessageContent(fitted, i, maxTokens, discoveryCompactionMarker)
		if err != nil {
			continue
		}
		if trimmed {
			report.ToolOutputsTrimmed++
		}
	}

	if estimateChatMessages(fitted) > maxTokens {
		cue := strings.TrimPrefix(fitted[1].Content, initialCore)
		fixedOnly := []chatMessage{fitted[0], {Role: fitted[1].Role, Content: initialCore}}
		if estimateChatMessages(fixedOnly) > maxTokens {
			return nil, report, fmt.Errorf("discovery context budget %d cannot fit fixed system/protocol content (%d estimated tokens)", maxTokens, estimateChatMessages(fixedOnly))
		}
		withoutCue := cloneChatMessages(fitted)
		withoutCue[1].Content = initialCore
		available := maxTokens - estimateChatMessages(withoutCue)
		if available < 0 {
			available = 0
		}
		fittedCue := ""
		truncated := strings.TrimSpace(cue) != ""
		var err error
		if available > 0 {
			fittedCue, truncated, err = llm.TruncateLinesKeepTail(cue, available, discoveryCueMarker)
			if err != nil {
				return nil, report, err
			}
		}
		fitted[1].Content = initialCore + fittedCue
		report.InitialCueTrimmed = truncated
	}

	// Nudge and assistant messages are normally tiny. As a final hard-cap
	// defense, retain their positions while tail-truncating their content.
	for i := 2; i < len(fitted) && estimateChatMessages(fitted) > maxTokens; i++ {
		if strings.HasPrefix(fitted[i].Content, discoveryStateSummaryPrefix) {
			continue
		}
		_, _ = fitMessageContent(fitted, i, maxTokens, discoveryCompactionMarker)
	}

	report.EstimatedTokens = estimateChatMessages(fitted)
	if report.EstimatedTokens > maxTokens {
		return nil, report, fmt.Errorf("discovery request exceeds context budget after compaction: estimated %d tokens, maximum %d", report.EstimatedTokens, maxTokens)
	}
	return fitted, report, nil
}
