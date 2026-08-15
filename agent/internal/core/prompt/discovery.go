package prompt

import (
	"context"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/core/discoveryrunner"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

// RunFileDiscovery executes file discovery and returns the discovered paths.
func RunFileDiscovery(ctx context.Context, prompt string, runtime ModelRuntime, fallbackRuntime ModelRuntime, trajectoryOverride, sessionID string, verbose bool, turnTimeout, contextLength int) ([]string, error) {
	fdRuntime := runtime
	if strings.TrimSpace(fdRuntime.Resolved.Model) == "" {
		fdRuntime = fallbackRuntime
	}
	budget, err := llm.ResolveInputBudgetForChain(fdRuntime.Resolved, fdRuntime.FallbackResolved, contextLength)
	if err != nil {
		return nil, err
	}
	fittedPrompt, truncated, err := discoveryrunner.FitInitialPrompt(prompt, budget)
	if err != nil {
		return nil, err
	}
	drModel := discoveryrunner.ModelSettings{
		UsingAlias:         fdRuntime.UsingAlias,
		Alias:              fdRuntime.Alias,
		Resolved:           fdRuntime.Resolved,
		Extras:             copyExtrasMap(fdRuntime.Extras),
		ParamPairs:         append([]string(nil), fdRuntime.ParamPairs...),
		ParamJSON:          append([]string(nil), fdRuntime.ParamJSON...),
		FallbackAliases:    append([]string(nil), fdRuntime.FallbackAliases...),
		FallbackResolved:   cloneResolvedModels(fdRuntime.FallbackResolved),
		TrajectoryOverride: strings.TrimSpace(trajectoryOverride),
		APIKeyOverrides:    llm.CopyAPIKeyOverridesForRuntime(fdRuntime.APIKeyOverrides),
		TurnTimeout:        turnTimeout,
		InputBudget:        budget,
		PromptTruncated:    truncated,
	}
	discoveryCtx := llm.WithStage(llm.WithAPIKeyOverrides(ctx, fdRuntime.APIKeyOverrides), "file-discovery")
	llm.EmitContextBudgetDetails(discoveryCtx, fdRuntime.Resolved, budget, map[string]any{"applied_timeout_sec": turnTimeout, "truncated": truncated})
	result, err := discoveryrunner.Run(discoveryCtx, fittedPrompt, drModel, sessionID, verbose)
	if err != nil {
		return nil, err
	}
	return result.Paths, nil
}
