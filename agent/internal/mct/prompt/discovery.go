package prompt

import (
	"context"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/discoveryrunner"
)

// RunFileDiscovery executes file discovery and returns the discovered paths.
func RunFileDiscovery(ctx context.Context, prompt string, runtime ModelRuntime, fallbackRuntime ModelRuntime, trajectoryOverride, sessionID string, verbose bool) ([]string, error) {
	fdRuntime := runtime
	if strings.TrimSpace(fdRuntime.Resolved.Model) == "" {
		fdRuntime = fallbackRuntime
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
	}
	discoveryCtx := llm.WithStage(llm.WithAPIKeyOverrides(ctx, fdRuntime.APIKeyOverrides), "file-discovery")
	result, err := discoveryrunner.Run(discoveryCtx, prompt, drModel, sessionID, verbose)
	if err != nil {
		return nil, err
	}
	return result.Paths, nil
}
