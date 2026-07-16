package integration

import (
	"context"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
	discoverypkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/discovery"
)

type Config = cfgpkg.Config

type LLMSettings = discoverypkg.LLMSettings

type ToolCallMode = cfgpkg.ToolCallMode

const (
	ToolCallModeJSON   = cfgpkg.ToolCallModeJSON
	ToolCallModeSimple = cfgpkg.ToolCallModeSimple
)

func Run(ctx context.Context, cfg Config, llmCfg LLMSettings) int {
	return discoverypkg.Run(ctx, cfg, llmCfg)
}

func RunEmbedded(ctx context.Context, cfg Config, llmCfg LLMSettings, initialPrompt string) int {
	return discoverypkg.RunEmbedded(ctx, cfg, llmCfg, initialPrompt)
}

func FixedInputTokens(mode ToolCallMode) int {
	return discoverypkg.FixedInputTokens(mode)
}
