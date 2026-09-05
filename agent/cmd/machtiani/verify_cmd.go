package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

type verificationRole struct {
	Name  string
	Alias string
}

type verificationRoleResult struct {
	Role             string `json:"role"`
	Alias            string `json:"alias"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int    `json:"inputTokens,omitempty"`
	OutputTokens     int    `json:"outputTokens,omitempty"`
	ReasoningTokens  int    `json:"reasoningTokens,omitempty"`
	CachedTokens     int    `json:"cachedTokens,omitempty"`
	CacheWriteTokens int    `json:"cacheWriteTokens,omitempty"`
	UsageAvailable   bool   `json:"usageAvailable"`
}

type verificationReport struct {
	Version int                      `json:"version"`
	Status  string                   `json:"status"`
	Roles   []verificationRoleResult `json:"roles"`
}

func configuredVerificationRoles(cfg llm.Config) []verificationRole {
	fallback := strings.TrimSpace(cfg.DefaultModel)
	alias := func(value string) string {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
		return fallback
	}
	return []verificationRole{
		{Name: "planner", Alias: fallback},
		{Name: "shell-agent", Alias: alias(cfg.ShellAgentModel)},
		{Name: "answer", Alias: alias(cfg.AnswerModel)},
		{Name: "file-discovery", Alias: alias(cfg.FileDiscoveryModel)},
	}
}

func handleVerifyCommand(args []string) int {
	flags := pflag.NewFlagSet("machtiani verify", pflag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	jsonOutput := flags.Bool("json", false, "write a machine-readable verification report")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum duration for each model-role probe")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: machtiani verify [--json] [--timeout <duration>]")
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if len(flags.Args()) != 0 {
		flags.Usage()
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "Verify error: --timeout must be greater than zero")
		return 2
	}

	cfg, _, err := llm.LoadGlobalConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Verify configuration error: %v\n", err)
		return 1
	}
	roles := configuredVerificationRoles(cfg)
	resolved := make(map[string]llm.ResolvedModel, len(roles))
	for _, role := range roles {
		if role.Alias == "" {
			fmt.Fprintf(os.Stderr, "Verify configuration error: %s has no configured model alias.\n", role.Name)
			return 1
		}
		model, err := llm.ResolveModel(role.Alias)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Verify configuration error for %s: %v\n", role.Name, err)
			return 1
		}
		if model.Transport != "model-host" {
			fmt.Fprintf(os.Stderr, "Verify configuration error: %s model %q does not use the shared model host.\n", role.Name, role.Alias)
			return 1
		}
		resolved[role.Name] = model
	}

	report := verificationReport{Version: 1, Status: "ok", Roles: make([]verificationRoleResult, 0, len(roles))}
	runID := uuid.NewString()
	for _, role := range roles {
		model := resolved[role.Name]
		probeCtx, cancel := context.WithTimeout(context.Background(), *timeout)
		probeCtx = llm.WithStage(probeCtx, role.Name)
		probeCtx = llm.WithModelHostSessionID(probeCtx, fmt.Sprintf("verification-%s-%s", runID, role.Name))
		var usage llm.UsageInfo
		probeCtx = llm.WithUsageObserver(probeCtx, func(_ llm.ResolvedModel, observed llm.UsageInfo) { usage = observed })
		reply, callErr := llm.ChatWithResolved(probeCtx, model, nil, []llm.Message{
			{Role: "system", Content: "This is a non-interactive Machtiani model transport verification. Do not use tools. Follow the user's response-format request."},
			{Role: "user", Content: fmt.Sprintf("Reply with the single word READY to verify the %s model role.", role.Name)},
		})
		cancel()
		if callErr != nil {
			fmt.Fprintf(os.Stderr, "Verify %s failed: %s\n", role.Name, actionableModelHostError(role.Alias, callErr))
			return 1
		}
		if !strings.Contains(strings.ToUpper(reply), "READY") {
			fmt.Fprintf(os.Stderr, "Verify %s failed: the provider did not return the expected confirmation.\n", role.Name)
			return 1
		}
		report.Roles = append(report.Roles, verificationRoleResult{
			Role: role.Name, Alias: role.Alias, Provider: model.ProviderName, Model: model.Model,
			InputTokens: usage.PromptTokens, OutputTokens: usage.CompletionTokens,
			ReasoningTokens: usage.ReasoningTokens,
			CachedTokens:    usage.CachedTokens, CacheWriteTokens: usage.CacheWriteTokens,
			UsageAvailable: usage.UsageAvailable,
		})
	}
	if *jsonOutput {
		if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
			fmt.Fprintln(os.Stderr, "Verify error: could not write the verification report.")
			return 1
		}
		return 0
	}
	for _, role := range report.Roles {
		fmt.Printf("%s: ok (%s/%s)\n", role.Role, role.Provider, role.Model)
	}
	return 0
}

func actionableModelHostError(alias string, err error) string {
	var hostErr *llm.ModelHostCallError
	if !errors.As(err, &hostErr) {
		return err.Error()
	}
	switch hostErr.Code {
	case "AUTH_REQUIRED", "AUTH_EXPIRED":
		return fmt.Sprintf("%s Run 'machtiani auth login --model %s' in an interactive terminal, then retry.", hostErr.Message, alias)
	case "RATE_LIMITED":
		if hostErr.RetryAfterMS > 0 {
			return fmt.Sprintf("%s Retry after %s.", hostErr.Message, (time.Duration(hostErr.RetryAfterMS) * time.Millisecond).Round(time.Second))
		}
	case "QUOTA_EXHAUSTED":
		return hostErr.Message + " Check the subscription account's usage limits before retrying."
	}
	return fmt.Sprintf("%s (%s)", hostErr.Message, hostErr.Code)
}
