package llm

// DefaultConfig returns a Config with safe defaults suitable as a base
// configuration before file-based and flag-based overlays are applied.
func DefaultConfig() Config {
	return Config{
		DefaultModel: "",
		Planner: &PlannerConfig{
			MaxTurns:      150,
			TurnTimeout:   120,
			MaxInputTokens: 180000,
		},
		ShellAgent: &ShellAgentConfig{
			MaxSteps:               110,
			FinalizeRemainingSteps: 10,
		},
		Environment: &EnvironmentConfig{
			Type:                  "local",
			CommandTimeout:       9999,
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
			"turn_timeout":     int64(120),
			"max_input_tokens": int64(180000),
		},
		"shell-agent": map[string]any{
			"max_steps":                int64(110),
			"finalize_remaining_steps": int64(10),
		},
		"environment": map[string]any{
			"type":                     "local",
			"command_timeout":         int64(9999),
			"cwd":                      ".",
			"max_command_output_bytes": int64(65536),
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
	overlayConfig(&clone, fileConfig)
	overlayConfig(&clone, flagOverrides)
	return clone
}

// overlayConfig overlays non-zero and non-nil fields from source onto target.
func overlayConfig(target *Config, source Config) {
	// --- string fields --------------------------------------------------
	if source.DefaultModel != "" {
		target.DefaultModel = source.DefaultModel
	}

	// --- planner --------------------------------------------------------
	if source.Planner != nil {
		if target.Planner == nil {
			target.Planner = &PlannerConfig{}
		}
		if source.Planner.maxTurnsSet || source.Planner.MaxTurns != 0 {
			target.Planner.MaxTurns = source.Planner.MaxTurns
		}
		if source.Planner.turnTimeoutSet || source.Planner.TurnTimeout != 0 {
			target.Planner.TurnTimeout = source.Planner.TurnTimeout
		}
		if source.Planner.maxInputTokensSet || source.Planner.MaxInputTokens != 0 {
			target.Planner.MaxInputTokens = source.Planner.MaxInputTokens
		}
	}

	// --- shell-agent ----------------------------------------------------
	if source.ShellAgent != nil {
		if target.ShellAgent == nil {
			target.ShellAgent = &ShellAgentConfig{}
		}
		if source.ShellAgent.MaxSteps != 0 {
			target.ShellAgent.MaxSteps = source.ShellAgent.MaxSteps
		}
		if source.ShellAgent.FinalizeRemainingSteps != 0 {
			target.ShellAgent.FinalizeRemainingSteps = source.ShellAgent.FinalizeRemainingSteps
		}
	}

	// --- environment ----------------------------------------------------
	if source.Environment != nil {
		if target.Environment == nil {
			target.Environment = &EnvironmentConfig{}
		}
		if source.Environment.Type != "" {
			target.Environment.Type = source.Environment.Type
		}
		if source.Environment.CWD != "" {
			target.Environment.CWD = source.Environment.CWD
		}
		if source.Environment.CommandTimeout != 0 {
			target.Environment.CommandTimeout = source.Environment.CommandTimeout
		}
		if source.Environment.MaxCommandOutputBytes != 0 {
			target.Environment.MaxCommandOutputBytes = source.Environment.MaxCommandOutputBytes
		}
	}

	// --- providers ------------------------------------------------------
	if len(source.Providers) > 0 {
		if target.Providers == nil {
			target.Providers = make(map[string]ProviderConfig)
		}
		for k, v := range source.Providers {
			if existing, ok := target.Providers[k]; ok {
				// overlay onto an already-present provider
				if v.BaseURL != "" {
					existing.BaseURL = v.BaseURL
				}
				if v.APIKey != "" {
					existing.APIKey = v.APIKey
				}
				if v.Endpoint != "" {
					existing.Endpoint = v.Endpoint
				}
				if len(v.Headers) > 0 {
					existing.Headers = copyStringMap(v.Headers)
				}
				if len(v.Query) > 0 {
					existing.Query = copyStringMap(v.Query)
				}
				target.Providers[k] = existing
			} else {
				// deep-copy a brand-new provider entry
				copyProv := ProviderConfig{
					BaseURL:  v.BaseURL,
					APIKey:   v.APIKey,
					Endpoint: v.Endpoint,
				}
				if len(v.Headers) > 0 {
					copyProv.Headers = copyStringMap(v.Headers)
				}
				if len(v.Query) > 0 {
					copyProv.Query = copyStringMap(v.Query)
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
		for k, v := range source.Models {
			if existing, ok := target.Models[k]; ok {
				// overlay onto an already-present model definition
				if v.Provider != "" {
					existing.Provider = v.Provider
				}
				if v.Model != "" {
					existing.Model = v.Model
				}
				if v.CacheKeyName != "" {
					existing.CacheKeyName = v.CacheKeyName
				}
				if len(v.CacheControl) > 0 {
					existing.CacheControl = deepCopyMap(v.CacheControl)
				}
				if v.CacheTriggerThreshold != 0 {
					existing.CacheTriggerThreshold = v.CacheTriggerThreshold
				}
				if v.CacheLookbackOffset != 0 {
					existing.CacheLookbackOffset = v.CacheLookbackOffset
				}
				if v.CacheReanchorTokens != 0 {
					existing.CacheReanchorTokens = v.CacheReanchorTokens
				}
				if v.CacheReanchorMessages != 0 {
					existing.CacheReanchorMessages = v.CacheReanchorMessages
				}
				if v.CacheReanchorMinCachedTokens != 0 {
					existing.CacheReanchorMinCachedTokens = v.CacheReanchorMinCachedTokens
				}
				if len(v.Params) > 0 {
					existing.Params = deepCopyMap(v.Params)
				}
				target.Models[k] = existing
			} else {
				// deep-copy a brand-new model definition
				copyModel := ModelDefinition{
					Provider:                     v.Provider,
					Model:                        v.Model,
					CacheKeyName:                 v.CacheKeyName,
					CacheControl:                 deepCopyMap(v.CacheControl),
					CacheTriggerThreshold:        v.CacheTriggerThreshold,
					CacheLookbackOffset:          v.CacheLookbackOffset,
					CacheReanchorTokens:          v.CacheReanchorTokens,
					CacheReanchorMessages:        v.CacheReanchorMessages,
					CacheReanchorMinCachedTokens: v.CacheReanchorMinCachedTokens,
				}
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
		}
		// booleans: always copy from source when non-nil (allows explicit false)
		target.Trajectory.Enabled = source.Trajectory.Enabled
		target.Trajectory.VerboseLLM = source.Trajectory.VerboseLLM
		target.Trajectory.StreamTokens = source.Trajectory.StreamTokens
		target.Trajectory.OmitRepoRoot = source.Trajectory.OmitRepoRoot
		if source.Trajectory.Excerpt != 0 {
			target.Trajectory.Excerpt = source.Trajectory.Excerpt
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
	}
	if source.PersistTmpData {
		target.PersistTmpData = source.PersistTmpData
	}
	if source.DryRun {
		target.DryRun = source.DryRun
	}
	if source.ShellAgentEnabled {
		target.ShellAgentEnabled = source.ShellAgentEnabled
	}
	if source.ShellAgentModel != "" {
		target.ShellAgentModel = source.ShellAgentModel
	}
	if source.AnswerModel != "" {
		target.AnswerModel = source.AnswerModel
	}
	if source.FileDiscoveryModel != "" {
		target.FileDiscoveryModel = source.FileDiscoveryModel
	}
	if source.AnswerTag != "" {
		target.AnswerTag = source.AnswerTag
	}
	if source.Tag != "" {
		target.Tag = source.Tag
	}
	if source.EnableTagFormat {
		target.EnableTagFormat = source.EnableTagFormat
	}
	if source.FinalFile != "" {
		target.FinalFile = source.FinalFile
	}
	if source.TranscriptFile != "" {
		target.TranscriptFile = source.TranscriptFile
	}
	if source.FileDiscoveryTrajectory != "" {
		target.FileDiscoveryTrajectory = source.FileDiscoveryTrajectory
	}
	if source.FileDiscoveryOutputDir != "" {
		target.FileDiscoveryOutputDir = source.FileDiscoveryOutputDir
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
