package integration

import (
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

func Run(cfg Config, llmCfg LLMSettings) int {
	return discoverypkg.Run(cfg, llmCfg)
}
