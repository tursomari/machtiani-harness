package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/presentation"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	runpkg "github.com/tursomari/machtiani/agent/internal/shell-agent/internal/run"
)

type multiString []string

const (
	shellAgentResultBlockBegin = "BEGIN_SHELL_AGENT_RESULT"
	shellAgentResultBlockEnd   = "END_SHELL_AGENT_RESULT"
)

func (m *multiString) String() string {
	return strings.Join(*m, ",")
}

func (m *multiString) Set(value string) error {
	*m = append(*m, value)
	return nil
}

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--max-input-tokens" || strings.HasPrefix(arg, "--max-input-tokens=") {
			fmt.Fprintln(os.Stderr, "--max-input-tokens was removed; use --context-length to set the total session context window")
			os.Exit(2)
		}
	}
	verbose := flag.Bool("verbose", false, "emit partial trajectories after each step")
	outputFormat := flag.String("output-format", "simple", "output format: simple, markdown, or json")
	shellAgentModel := flag.String("shell-agent-model", "", "model alias override for shell-agent (default: config)")
	contextLength := flag.Int("context-length", 0, "total input-plus-output token context for this session")
	sessionID := flag.String("session-id", "", "session ID for state persistence and resume")
	// Deprecated: use --tag instead, which sets both answer and command tag suffixes.
	answerTag := flag.String("answer-tag", "", `Override the final-answer tag name used by the parser and prompt templates. Must not contain "<", ">", "/", "{{", or "}}". Empty input keeps the default ("answer").`)
	tagSuffix := flag.String("tag", "", "single suffix for both answer and command tags (e.g. --tag foo produces answer-foo and command-foo)")
	var apiKeyOverrideFlags multiString
	flag.Var(&apiKeyOverrideFlags, "api-key", "Provider-specific API key override (provider:key; repeatable)")
	flag.Parse()
	if *tagSuffix != "" && *answerTag != "" {
		fmt.Fprintln(os.Stderr, "cannot set both --tag and --answer-tag")
		os.Exit(2)
	}
	effectiveAnswerTag := *answerTag
	if *tagSuffix != "" {
		effectiveAnswerTag = "answer-" + *tagSuffix
	}
	if err := shellagent.ValidateAnswerTag(effectiveAnswerTag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	// Derive effective command tag from --tag suffix.
	// If --tag is set the command tag becomes "command-<suffix>";
	// otherwise it stays "command".
	effectiveCommandTag := "command"
	if *tagSuffix != "" {
		effectiveCommandTag = "command-" + *tagSuffix
	}
	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: shell-agent \"<your prompt>\"")
		os.Exit(1)
	}

	task := args[0]
	if *contextLength != 0 && *contextLength < llm.MinimumContextLength {
		fmt.Fprintf(os.Stderr, "context-length must be at least %d\n", llm.MinimumContextLength)
		os.Exit(2)
	}
	apiOverrides, err := llm.ParseAPIKeyOverrides(apiKeyOverrideFlags)
	if err != nil {
		log.Fatalf("parse api-key overrides: %v", err)
	}
	globalCfg, configPath, err := llm.LoadGlobalConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	persistTmpEnv := strings.TrimSpace(os.Getenv("MACHTIANI_PERSIST_TMP_DATA"))
	persistTmpData := false
	if persistTmpEnv != "" {
		switch strings.ToLower(persistTmpEnv) {
		case "1", "true", "yes", "on":
			persistTmpData = true
		}
	}

	lib, err := shellagent.BuildLibrary(&globalCfg, apiOverrides, persistTmpData, strings.TrimSpace(*shellAgentModel), effectiveAnswerTag, effectiveCommandTag)
	if err != nil {
		log.Fatalf("build shell-agent library (%s): %v", configPath, err)
	}
	resolved, err := llm.ResolveModelWithOverrides(strings.TrimSpace(*shellAgentModel), apiOverrides)
	if err != nil {
		log.Fatalf("resolve context model: %v", err)
	}
	budget, err := llm.ResolveInputBudget(resolved, *contextLength)
	if err != nil {
		log.Fatalf("resolve context budget: %v", err)
	}
	if closer, ok := lib.Env.(interface{ Close() error }); ok {
		defer func() {
			if err := closer.Close(); err != nil {
				log.Printf("warning: environment cleanup failed: %v", err)
			}
		}()
	}

	// Build the message array: system prompt + instance prompt.
	systemVars := map[string]interface{}{"cwd": lib.CWD, "CWD": lib.CWD}
	sysPrompt, err := shellagent.RenderSystemPrompt(lib.Prompts, systemVars, effectiveAnswerTag, effectiveCommandTag)
	if err != nil {
		log.Fatalf("render system prompt: %v", err)
	}
	instPrompt, err := shellagent.RenderInstancePrompt(lib.Prompts, task, lib.Config, lib.Env, nil, effectiveAnswerTag, effectiveCommandTag)
	if err != nil {
		log.Fatalf("render instance prompt: %v", err)
	}
	prebuilt := []llm.Message{
		{Role: "system", Content: sysPrompt},
		{Role: "user", Content: instPrompt},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	req := shellagent.Request{
		PreconstructedMessages: prebuilt,
		Config:                 lib.Config,
		Prompts:                lib.Prompts,
		Model:                  lib.Model,
		Env:                    lib.Env,
		Verbose:                *verbose,
		MaxInputTokens:         budget.MaxInputTokens,
		SessionID:              *sessionID,
		AnswerTag:              effectiveAnswerTag,
		CommandTag:             effectiveCommandTag,
	}
	res, runErr := shellagent.Run(ctx, req)
	exitStatus := res.ExitStatus
	result := res.Answer
	exitCode := 0
	if runErr != nil {
		if exitStatus == "" {
			exitStatus = "Error"
			exitCode = 1
		}
		if result == "" {
			result = runErr.Error()
		}
		log.Printf("agent run failed: %v", runErr)
	}

	timestamp := time.Now().UTC().Format("20060102-150405")
	scratchDir := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_TEMP_ROOT"))
	if scratchDir == "" {
		scratchDir = os.TempDir()
	}
	trajPath := filepath.Join(scratchDir, fmt.Sprintf("trajectory-%s.json", timestamp))
	traj := runpkg.FileTrajectory{
		Messages:   res.Trajectory.Messages,
		ExitStatus: exitStatus,
		Result:     result,
		ExtraInfo:  res.Trajectory.ExtraInfo,
	}
	if err := runpkg.SaveTrajectory(traj, trajPath); err != nil {
		log.Printf("warning: failed to save trajectory: %v", err)
	}

	fmt.Println(shellAgentResultBlockBegin)
	fmt.Printf("Exit Status: %s\n", exitStatus)
	fmt.Printf("Result: %s\n", result)
	fmt.Println(shellAgentResultBlockEnd)

	switch strings.ToLower(*outputFormat) {
	case "simple":
		themeName := string(presentation.ProfileTerminal)
		glyphMode := string(presentation.GlyphUnicode)
		if globalCfg.UI != nil {
			themeName = globalCfg.UI.Theme
			glyphMode = globalCfg.UI.Glyphs
		}
		uiTheme, themeErr := presentation.ResolveWithGlyphs(themeName, glyphMode, os.Stdout)
		if themeErr != nil {
			log.Printf("warning: failed to resolve UI presentation: %v", themeErr)
		}
		transcript := runpkg.RenderSimpleTranscript(traj, uiTheme.Glyphs())
		fmt.Println()
		fmt.Println(transcript)
	case "markdown", "md":
		transcript := runpkg.RenderCleanTranscript(traj)
		fmt.Println()
		fmt.Println(transcript)
	case "json":
		trajJSON, err := runpkg.MarshalTrajectory(traj)
		if err != nil {
			log.Printf("warning: failed to marshal trajectory: %v", err)
			break
		}
		fmt.Println()
		fmt.Println("Full Trajectory:")
		fmt.Println(string(trajJSON))
	default:
		log.Fatalf("unsupported output format %q (expected simple, markdown, or json)", *outputFormat)
	}

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
