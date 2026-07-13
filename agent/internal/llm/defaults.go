package llm

// DefaultConfig returns a Config with safe defaults suitable as a base
// configuration before file-based and flag-based overlays are applied.
func DefaultConfig() Config {
	return Config{
		DefaultModel: "",
		Planner: &PlannerConfig{
			MaxTurns:       150,
			TurnTimeout:    0,
			MaxInputTokens: 180000,
		},
		ShellAgent: &ShellAgentConfig{
			MaxSteps:               110,
			FinalizeRemainingSteps: 10,
		},
		Environment: &EnvironmentConfig{
			Type:                  "local",
			CommandTimeout:        9999,
			CWD:                   ".",
			MaxCommandOutputBytes: 65536,
		},
		Trajectory: &TrajectoryConfig{
			Enabled:      true,
			File:         "",
			VerboseLLM:   false,
			StreamTokens: false,
			Excerpt:      512,
			OmitRepoRoot: false,
		},
		UI:                      &UIConfig{Theme: "terminal"},
		Verbose:                 false,
		PersistTmpData:          false,
		DryRun:                  false,
		ShellAgentEnabled:       false,
		ShellAgentModel:         "",
		AnswerModel:             "",
		FileDiscoveryModel:      "",
		AnswerTag:               "",
		Tag:                     "",
		EnableTagFormat:         false,
		FinalFile:               "",
		TranscriptFile:          "",
		FileDiscoveryTrajectory: "",
		FileDiscoveryOutputDir:  "",
		Prompts:                 nil,
		Mode:                    nil,
		Providers:               nil,
		Models:                  nil,
	}
}

// DefaultMinimalConfigMap returns a map representation of the minimal default
// configuration. The returned map uses int64 for numeric values so that callers
// can encode it to TOML (or another format) without ambiguity.
func DefaultMinimalConfigMap() map[string]any {
	return map[string]any{
		"default_model": "",
		"planner": map[string]any{
			"max_turns":        int64(150),
			"turn_timeout":     int64(0),
			"max_input_tokens": int64(180000),
		},
		"shell-agent": map[string]any{
			"max_steps":                int64(110),
			"finalize_remaining_steps": int64(10),
		},
		"environment": map[string]any{
			"type":                     "local",
			"command_timeout":          int64(9999),
			"cwd":                      ".",
			"max_command_output_bytes": int64(65536),
		},
		"ui": map[string]any{
			"theme": "terminal",
		},
		"providers": map[string]any{},
		"models":    map[string]any{},
	}
}

// MergeConfig combines a defaults Config with file-based overrides and
// flag-based overrides.  The defaults are deep-copied before overlays are
// applied so that the caller retains ownership of the original.
func MergeConfig(defaults, fileConfig, flagOverrides Config) Config {
	clone := cloneConfig(defaults)
	overlayConfig(&clone, fileConfig, SourceFile)
	overlayConfig(&clone, flagOverrides, SourceFlag)
	return clone
}

// overlayConfig overlays non-zero and non-nil fields from source onto target.
func overlayConfig(target *Config, source Config, srcSource FieldSource) {
	// --- string fields --------------------------------------------------
	if source.DefaultModel != "" {
		target.DefaultModel = source.DefaultModel
		target.DefaultModelSource = srcSource
	}

	// --- planner --------------------------------------------------------
	if source.Planner != nil {
		if target.Planner == nil {
			target.Planner = &PlannerConfig{}
		}
		if source.Planner.maxTurnsSet || source.Planner.MaxTurns != 0 {
			target.Planner.MaxTurns = source.Planner.MaxTurns
			target.Planner.MaxTurnsSource = srcSource
		}
		if source.Planner.turnTimeoutSet || source.Planner.TurnTimeout != 0 {
			target.Planner.TurnTimeout = source.Planner.TurnTimeout
			target.Planner.TurnTimeoutSource = srcSource
		}
		if source.Planner.maxInputTokensSet || source.Planner.MaxInputTokens != 0 {
			target.Planner.MaxInputTokens = source.Planner.MaxInputTokens
			target.Planner.MaxInputTokensSource = srcSource
		}
	}

	// --- shell-agent ----------------------------------------------------
	if source.ShellAgent != nil {
		if target.ShellAgent == nil {
			target.ShellAgent = &ShellAgentConfig{}
		}
		if source.ShellAgent.MaxSteps != 0 {
			target.ShellAgent.MaxSteps = source.ShellAgent.MaxSteps
			target.ShellAgent.MaxStepsSource = srcSource
		}
		if source.ShellAgent.FinalizeRemainingSteps != 0 {
			target.ShellAgent.FinalizeRemainingSteps = source.ShellAgent.FinalizeRemainingSteps
			target.ShellAgent.FinalizeRemainingStepsSource = srcSource
		}
	}

	// --- environment ----------------------------------------------------
	if source.Environment != nil {
		if target.Environment == nil {
			target.Environment = &EnvironmentConfig{}
		}
		if source.Environment.Type != "" {
			target.Environment.Type = source.Environment.Type
			target.Environment.TypeSource = srcSource
		}
		if source.Environment.CWD != "" {
			target.Environment.CWD = source.Environment.CWD
			target.Environment.CWDSource = srcSource
		}
		if source.Environment.CommandTimeout != 0 {
			target.Environment.CommandTimeout = source.Environment.CommandTimeout
			target.Environment.CommandTimeoutSource = srcSource
		}
		if source.Environment.MaxCommandOutputBytes != 0 {
			target.Environment.MaxCommandOutputBytes = source.Environment.MaxCommandOutputBytes
			target.Environment.MaxCommandOutputBytesSource = srcSource
		}
	}

	// --- user interface -------------------------------------------------
	if source.UI != nil {
		if target.UI == nil {
			target.UI = &UIConfig{}
		}
		if source.UI.Theme != "" {
			target.UI.Theme = source.UI.Theme
			target.UI.ThemeSource = srcSource
		}
	}

	// --- model defaults -------------------------------------------------
	if source.ModelDefaults != nil {
		if target.ModelDefaults == nil {
			copyDefaults := *source.ModelDefaults
			copyDefaults.CacheControl = deepCopyMap(source.ModelDefaults.CacheControl)
			target.ModelDefaults = &copyDefaults
		} else {
			dst, src := target.ModelDefaults, source.ModelDefaults
			if src.cacheEnabledSet || src.CacheEnabled {
				dst.CacheEnabled, dst.cacheEnabledSet = src.CacheEnabled, true
			}
			if src.cacheKeyNameSet || src.CacheKeyName != "" {
				dst.CacheKeyName, dst.cacheKeyNameSet = src.CacheKeyName, true
			}
			if src.cacheControlSet || len(src.CacheControl) > 0 {
				dst.CacheControl, dst.cacheControlSet = deepCopyMap(src.CacheControl), true
			}
			if src.cacheTriggerThresholdSet || src.CacheTriggerThreshold != 0 {
				dst.CacheTriggerThreshold, dst.cacheTriggerThresholdSet = src.CacheTriggerThreshold, true
			}
			if src.cacheLookbackOffsetSet || src.CacheLookbackOffset != 0 {
				dst.CacheLookbackOffset, dst.cacheLookbackOffsetSet = src.CacheLookbackOffset, true
			}
			if src.cacheReanchorTokensSet || src.CacheReanchorTokens != 0 {
				dst.CacheReanchorTokens, dst.cacheReanchorTokensSet = src.CacheReanchorTokens, true
			}
			if src.cacheReanchorMessagesSet || src.CacheReanchorMessages != 0 {
				dst.CacheReanchorMessages, dst.cacheReanchorMessagesSet = src.CacheReanchorMessages, true
			}
			if src.cacheReanchorMinCachedTokensSet || src.CacheReanchorMinCachedTokens != 0 {
				dst.CacheReanchorMinCachedTokens, dst.cacheReanchorMinCachedTokensSet = src.CacheReanchorMinCachedTokens, true
			}
		}
	}

	// --- providers ------------------------------------------------------
	if len(source.Providers) > 0 {
		if target.Providers == nil {
			target.Providers = make(map[string]ProviderConfig)
		}
		if target.ProviderSources == nil {
			target.ProviderSources = make(map[string]FieldSource)
		}
		for k, v := range source.Providers {
			target.ProviderSources[k] = srcSource
			if existing, ok := target.Providers[k]; ok {
				// overlay onto an already-present provider
				if v.BaseURL != "" {
					existing.BaseURL = v.BaseURL
					existing.BaseURLSource = srcSource
				}
				if v.APIKey != "" {
					existing.APIKey = v.APIKey
					existing.APIKeySource = srcSource
				}
				if v.Endpoint != "" {
					existing.Endpoint = v.Endpoint
					existing.EndpointSource = srcSource
				}
				if len(v.Headers) > 0 {
					existing.Headers = copyStringMap(v.Headers)
					existing.HeadersSource = srcSource
				}
				if len(v.Query) > 0 {
					existing.Query = copyStringMap(v.Query)
					existing.QuerySource = srcSource
				}
				target.Providers[k] = existing
			} else {
				// deep-copy a brand-new provider entry
				copyProv := ProviderConfig{
					BaseURL:  v.BaseURL,
					APIKey:   v.APIKey,
					Endpoint: v.Endpoint,
				}
				copyProv.BaseURLSource = srcSource
				copyProv.APIKeySource = srcSource
				copyProv.EndpointSource = srcSource
				if len(v.Headers) > 0 {
					copyProv.Headers = copyStringMap(v.Headers)
					copyProv.HeadersSource = srcSource
				}
				if len(v.Query) > 0 {
					copyProv.Query = copyStringMap(v.Query)
					copyProv.QuerySource = srcSource
				}
				target.Providers[k] = copyProv
			}
		}
	}

	// --- models ---------------------------------------------------------
	if len(source.Models) > 0 {
		if target.Models == nil {
			target.Models = make(map[string]ModelDefinition)
		}
		if target.ModelSources == nil {
			target.ModelSources = make(map[string]FieldSource)
		}
		for k, v := range source.Models {
			target.ModelSources[k] = srcSource
			if existing, ok := target.Models[k]; ok {
				// overlay onto an already-present model definition
				if v.Provider != "" {
					existing.Provider = v.Provider
					existing.ProviderSource = srcSource
				}
				if v.Model != "" {
					existing.Model = v.Model
					existing.ModelSource = srcSource
				}
				if v.cacheEnabledSet || v.CacheEnabled {
					existing.CacheEnabled = v.CacheEnabled
					existing.cacheEnabledSet = true
				}
				if v.cacheKeyNameSet || v.CacheKeyName != "" {
					existing.CacheKeyName = v.CacheKeyName
					existing.cacheKeyNameSet = true
					existing.CacheKeyNameSource = srcSource
				}
				if v.cacheControlSet || len(v.CacheControl) > 0 {
					existing.CacheControl = deepCopyMap(v.CacheControl)
					existing.cacheControlSet = true
					existing.CacheControlSource = srcSource
				}
				if v.cacheTriggerThresholdSet || v.CacheTriggerThreshold != 0 {
					existing.CacheTriggerThreshold = v.CacheTriggerThreshold
					existing.cacheTriggerThresholdSet = true
					existing.CacheTriggerThresholdSource = srcSource
				}
				if v.cacheLookbackOffsetSet || v.CacheLookbackOffset != 0 {
					existing.CacheLookbackOffset = v.CacheLookbackOffset
					existing.cacheLookbackOffsetSet = true
					existing.CacheLookbackOffsetSource = srcSource
				}
				if v.cacheReanchorTokensSet || v.CacheReanchorTokens != 0 {
					existing.CacheReanchorTokens = v.CacheReanchorTokens
					existing.cacheReanchorTokensSet = true
					existing.CacheReanchorTokensSource = srcSource
				}
				if v.cacheReanchorMessagesSet || v.CacheReanchorMessages != 0 {
					existing.CacheReanchorMessages = v.CacheReanchorMessages
					existing.cacheReanchorMessagesSet = true
					existing.CacheReanchorMessagesSource = srcSource
				}
				if v.cacheReanchorMinCachedTokensSet || v.CacheReanchorMinCachedTokens != 0 {
					existing.CacheReanchorMinCachedTokens = v.CacheReanchorMinCachedTokens
					existing.cacheReanchorMinCachedTokensSet = true
					existing.CacheReanchorMinCachedTokensSource = srcSource
				}
				if len(v.Params) > 0 {
					existing.Params = deepCopyMap(v.Params)
					existing.ParamsSource = srcSource
				}
				target.Models[k] = existing
			} else {
				// deep-copy a brand-new model definition
				copyModel := ModelDefinition{
					Provider:                        v.Provider,
					Model:                           v.Model,
					CacheKeyName:                    v.CacheKeyName,
					CacheControl:                    deepCopyMap(v.CacheControl),
					CacheTriggerThreshold:           v.CacheTriggerThreshold,
					CacheLookbackOffset:             v.CacheLookbackOffset,
					CacheReanchorTokens:             v.CacheReanchorTokens,
					CacheReanchorMessages:           v.CacheReanchorMessages,
					CacheReanchorMinCachedTokens:    v.CacheReanchorMinCachedTokens,
					CacheEnabled:                    v.CacheEnabled,
					cacheEnabledSet:                 v.cacheEnabledSet,
					cacheKeyNameSet:                 v.cacheKeyNameSet,
					cacheControlSet:                 v.cacheControlSet,
					cacheTriggerThresholdSet:        v.cacheTriggerThresholdSet,
					cacheLookbackOffsetSet:          v.cacheLookbackOffsetSet,
					cacheReanchorTokensSet:          v.cacheReanchorTokensSet,
					cacheReanchorMessagesSet:        v.cacheReanchorMessagesSet,
					cacheReanchorMinCachedTokensSet: v.cacheReanchorMinCachedTokensSet,
				}
				copyModel.ProviderSource = srcSource
				copyModel.ModelSource = srcSource
				copyModel.CacheKeyNameSource = srcSource
				copyModel.CacheControlSource = srcSource
				copyModel.CacheTriggerThresholdSource = srcSource
				copyModel.CacheLookbackOffsetSource = srcSource
				copyModel.CacheReanchorTokensSource = srcSource
				copyModel.CacheReanchorMessagesSource = srcSource
				copyModel.CacheReanchorMinCachedTokensSource = srcSource
				copyModel.ParamsSource = srcSource
				if len(v.Params) > 0 {
					copyModel.Params = deepCopyMap(v.Params)
				}
				target.Models[k] = copyModel
			}
		}
	}

	// --- trajectory -----------------------------------------------------
	if source.Trajectory != nil {
		if target.Trajectory == nil {
			target.Trajectory = &TrajectoryConfig{}
		}
		if source.Trajectory.File != "" {
			target.Trajectory.File = source.Trajectory.File
			target.Trajectory.FileSource = srcSource
		}
		// booleans: always copy from source when non-nil (allows explicit false)
		target.Trajectory.Enabled = source.Trajectory.Enabled
		target.Trajectory.EnabledSource = srcSource
		target.Trajectory.VerboseLLM = source.Trajectory.VerboseLLM
		target.Trajectory.VerboseLLMSource = srcSource
		target.Trajectory.StreamTokens = source.Trajectory.StreamTokens
		target.Trajectory.StreamTokensSource = srcSource
		target.Trajectory.OmitRepoRoot = source.Trajectory.OmitRepoRoot
		target.Trajectory.OmitRepoRootSource = srcSource
		if source.Trajectory.Excerpt != 0 {
			target.Trajectory.Excerpt = source.Trajectory.Excerpt
			target.Trajectory.ExcerptSource = srcSource
		}
	}

	// --- other pointer fields -------------------------------------------

	// Prompts
	if source.Prompts != nil {
		if target.Prompts == nil {
			p := *source.Prompts
			target.Prompts = &p
		} else {
			*target.Prompts = *source.Prompts
		}
	}

	// --- top-level behavioral fields ------------------------------------
	if source.Verbose {
		target.Verbose = source.Verbose
		target.VerboseSource = srcSource
	}
	if source.PersistTmpData {
		target.PersistTmpData = source.PersistTmpData
		target.PersistTmpDataSource = srcSource
	}
	if source.DryRun {
		target.DryRun = source.DryRun
		target.DryRunSource = srcSource
	}
	if source.ShellAgentEnabled {
		target.ShellAgentEnabled = source.ShellAgentEnabled
		target.ShellAgentEnabledSource = srcSource
	}
	if source.ShellAgentModel != "" {
		target.ShellAgentModel = source.ShellAgentModel
		target.ShellAgentModelSource = srcSource
	}
	if source.AnswerModel != "" {
		target.AnswerModel = source.AnswerModel
		target.AnswerModelSource = srcSource
	}
	if source.FileDiscoveryModel != "" {
		target.FileDiscoveryModel = source.FileDiscoveryModel
		target.FileDiscoveryModelSource = srcSource
	}
	if source.AnswerTag != "" {
		target.AnswerTag = source.AnswerTag
		target.AnswerTagSource = srcSource
	}
	if source.Tag != "" {
		target.Tag = source.Tag
		target.TagSource = srcSource
	}
	if source.EnableTagFormat {
		target.EnableTagFormat = source.EnableTagFormat
		target.EnableTagFormatSource = srcSource
	}
	if source.FinalFile != "" {
		target.FinalFile = source.FinalFile
		target.FinalFileSource = srcSource
	}
	if source.TranscriptFile != "" {
		target.TranscriptFile = source.TranscriptFile
		target.TranscriptFileSource = srcSource
	}
	if source.FileDiscoveryTrajectory != "" {
		target.FileDiscoveryTrajectory = source.FileDiscoveryTrajectory
		target.FileDiscoveryTrajectorySource = srcSource
	}
	if source.FileDiscoveryOutputDir != "" {
		target.FileDiscoveryOutputDir = source.FileDiscoveryOutputDir
		target.FileDiscoveryOutputDirSource = srcSource
	}

	// Mode
	if source.Mode != nil {
		if target.Mode == nil {
			m := *source.Mode
			if len(m.Modes) > 0 {
				m.Modes = copyModeMap(m.Modes)
			}
			target.Mode = &m
		} else {
			*target.Mode = *source.Mode
			if len(source.Mode.Modes) > 0 {
				target.Mode.Modes = copyModeMap(source.Mode.Modes)
			}
		}
	}
}
