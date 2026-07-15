package llm

import (
	"context"
	"fmt"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

const MinimumContextLength = 4096

type ContextSource string

const (
	ContextSourceSessionFlag     ContextSource = "session_flag"
	ContextSourceModel           ContextSource = "model"
	ContextSourceModelDefault    ContextSource = "model_default"
	ContextSourceCompiledDefault ContextSource = "compiled_default"
	ContextSourceRuntimeLearned  ContextSource = "runtime_learned"
)

// InputBudget is the single context policy used by request packers. ContextLength
// is the total input-plus-output window; MaxInputTokens is the derived input cap.
type InputBudget struct {
	ContextLength     int
	CompletionReserve int
	SafetyMargin      int
	MaxInputTokens    int
	Source            ContextSource
}

func ResolveInputBudget(model ResolvedModel, sessionContextLength int) (InputBudget, error) {
	length := model.ContextLength
	source := ContextSourceModelDefault
	if model.ContextLearned {
		source = ContextSourceRuntimeLearned
	} else if model.ContextSource == SourceDefault {
		source = ContextSourceCompiledDefault
	} else if !model.ContextInherited {
		source = ContextSourceModel
	}
	if length == 0 {
		length = DefaultContextLength
		source = ContextSourceCompiledDefault
	}
	if sessionContextLength != 0 {
		length = sessionContextLength
		source = ContextSourceSessionFlag
	}
	return BudgetForContextLength(length, source)
}

func BudgetForContextLength(length int, source ContextSource) (InputBudget, error) {
	if length < MinimumContextLength {
		return InputBudget{}, fmt.Errorf("context length must be at least %d total tokens", MinimumContextLength)
	}
	reserve := min(16384, length/8)
	margin := max(2048, (length+19)/20)
	usable := length - reserve - margin
	if usable <= 0 {
		return InputBudget{}, fmt.Errorf("context length %d leaves no usable input budget", length)
	}
	return InputBudget{ContextLength: length, CompletionReserve: reserve, SafetyMargin: margin, MaxInputTokens: usable, Source: source}, nil
}

// ContextLengthForInputCap returns the smallest valid total context whose
// derived usable input is at least the requested cap.
func ContextLengthForInputCap(input int) int {
	if input <= 0 {
		return MinimumContextLength
	}
	low, high := MinimumContextLength, max(DefaultContextLength, input*2)
	for {
		budget, _ := BudgetForContextLength(high, ContextSourceRuntimeLearned)
		if budget.MaxInputTokens >= input {
			break
		}
		high *= 2
	}
	for low < high {
		mid := low + (high-low)/2
		budget, _ := BudgetForContextLength(mid, ContextSourceRuntimeLearned)
		if budget.MaxInputTokens >= input {
			high = mid
		} else {
			low = mid + 1
		}
	}
	return low
}

func EmitContextBudget(ctx context.Context, model ResolvedModel, budget InputBudget) {
	writer, ok := trajectory.FromContext(ctx)
	if !ok || writer == nil {
		return
	}
	parent, _ := trajectory.ParentSpanID(ctx)
	_ = writer.Emit(ctx, trajectory.Event{Kind: "llm.context_budget.resolved", ParentSpanID: parent, Payload: map[string]any{
		"event_version":      1,
		"model_alias":        model.Alias,
		"provider":           model.ProviderName,
		"context_length":     budget.ContextLength,
		"source":             string(budget.Source),
		"completion_reserve": budget.CompletionReserve,
		"safety_margin":      budget.SafetyMargin,
		"max_input_tokens":   budget.MaxInputTokens,
	}})
}

func EmitContextAdjustment(ctx context.Context, model ResolvedModel, previous, learned int, persisted bool, persistErr error) {
	writer, ok := trajectory.FromContext(ctx)
	if !ok || writer == nil {
		return
	}
	parent, _ := trajectory.ParentSpanID(ctx)
	payload := map[string]any{
		"event_version":           1,
		"model_alias":             model.Alias,
		"provider":                model.ProviderName,
		"previous_context_length": previous,
		"learned_context_length":  learned,
		"persisted":               persisted,
	}
	if persistErr != nil {
		payload["persistence_error"] = persistErr.Error()
	}
	_ = writer.Emit(ctx, trajectory.Event{Kind: "llm.context_budget.adjusted", ParentSpanID: parent, Payload: payload})
}
