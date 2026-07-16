// Package shellagent provides the library entry point for running the
// shell-agent in-process. It replaces the previous subprocess-based
// integration (invokeShellAgent / exec.CommandContext) with a direct
// function call that accepts pre-built LLM messages.
package shellagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/agents"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/environments"
	shellmodels "github.com/tursomari/machtiani/agent/internal/shell-agent/internal/models"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/internal/run"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// Request carries the fully assembled message array and configuration
// needed to run the shell-agent in-process.
type Request struct {
	// PreconstructedMessages is the complete message array. It must
	// include the shell-agent system prompt as the first message,
	// followed by planner conversation messages, and ending with the
	// instance prompt (the current task). All messages are passed
	// directly to the agent without modification.
	PreconstructedMessages []llm.Message

	// Task is the current shell-agent work request. The library path
	// passes it separately from PreconstructedMessages so templates
	// rendered during the loop, such as forced finalization, can refer
	// to the original work request.
	Task string

	// Config is the shell-agent configuration (step limits, etc.).
	Config *minisweagent.ShellAgentConfig

	// Prompts holds the prompt template configuration.
	Prompts *minisweagent.PromptsConfig

	// Model is the LLM model adapter.
	Model minisweagent.Model

	// Env is the execution environment.
	Env minisweagent.Environment

	// Verbose enables verbose logging.
	Verbose bool

	// MaxInputTokens caps the token budget for planner prompts.
	MaxInputTokens int

	// SessionID identifies the session for state persistence and resume.
	// When set, the agent persists state after each step and can resume
	// from a previous session.
	SessionID string

	// PlannerTurn is the 0-based planner turn index for the work
	// request that produced this shell-agent invocation. It is stable
	// across format-error retries within the same work_request, so it
	// must be used (not the shell-agent internal stepCounter) for any
	// per-turn gating of behaviour. The shell-agent library path
	// constructs a fresh DefaultAgent per work_request, so callers
	// must populate this field before calling Run.
	PlannerTurn int

	// EnforceEarlyCommands enables stricter enforcement of the
	// <command>...</command> format on the first few planner turns.
	// When true and PlannerTurn < 3, the shell-agent:
	//   * emits a stricter, turn-specific format-error message instead
	//     of the generic FormatErrorTemplate fallback, and
	//   * validates the extracted command with `bash -n` to reject
	//     syntactically invalid bash before execution.
	// The flag is feature-gated: default behaviour is unchanged.
	EnforceEarlyCommands bool

	// AnswerTag overrides the final-answer tag name used by the
	// parser and the prompt templates. Empty input is normalised to
	// "answer" (the default) so callers that don't care about the
	// override can leave it unset. Validation of the tag name is the
	// caller's responsibility — see ValidateAnswerTag.
	AnswerTag string

	// CommandTag overrides the command tag name used by the parser
	// and prompt templates. Defaults to "command".
	CommandTag string

	// ExtraInstructions are appended to the system prompt for the
	// current run. When resuming, they are compared against the
	// saved SystemPrompt to detect mode mismatches.
	ExtraInstructions string

	// WarningWriter receives a warning message when a mode mismatch
	// is detected between the saved resume state and the current
	// ExtraInstructions. When nil, warnings are discarded.
	WarningWriter io.Writer

	// ResumeAttempt controls whether the agent attempts to resume
	// from a prior session state file. When true (the zero value),
	// existing behavior is preserved and the agent checks for a
	// resume file. When false, the agent skips the resume file
	// check entirely and always starts a fresh run, optionally
	// deleting any existing resume file for the SessionID.
	ResumeAttempt bool `json:"resume_attempt"`

	// InterruptStep causes the agent to checkpoint and stop after
	// this many successful steps. A value <= 0 means no interrupt.
	InterruptStep int

	// TrajectoryBaseDir overrides the directory used for trajectory
	// file storage. When set, the trajectory path is computed as
	// filepath.Join(baseDir, "trajectory.json") instead of using
	// artifacts.ShellAgentTrajectoryPath.
	TrajectoryBaseDir string `json:"trajectory_base_dir"`
}

// Result captures the outcome of a shell-agent run.
type Result struct {
	// Answer is the final answer text produced by the agent.
	Answer string

	// ExitStatus is the termination status (e.g. "Submitted", "LimitsExceeded").
	ExitStatus string

	// Error is any non-agent error that occurred during the run.
	Error error

	// TrajectoryPath is the directory path where the trajectory is stored.
	TrajectoryPath string

	// Trajectory is the full trajectory of the run, if available.
	Trajectory minisweagent.Trajectory
}

// Run executes the shell-agent loop using the provided request.
//
// The caller is responsible for building the complete message array:
//  1. Shell-agent system prompt (rendered once per session).
//  2. Planner conversation messages (serialized via serializeChatMessage).
//  3. Instance prompt (rendered from the shell-agent instance template).
//
// Run sets the messages and enters the standard tool-calling loop.
func Run(ctx context.Context, req Request) (Result, error) {
	var res Result

	// Compute shell-agent session checkpoint directory.
	opts := []agents.DefaultAgentOption{
		agents.WithVerbose(req.Verbose),
		agents.WithMaxInputTokens(req.MaxInputTokens),
		agents.WithTask(req.Task),
		agents.WithPlannerTurn(req.PlannerTurn),
		agents.WithEnforceEarlyCommands(req.EnforceEarlyCommands),
		agents.WithAnswerTag(req.AnswerTag),
		agents.WithCommandTag(req.CommandTag),
		func() agents.DefaultAgentOption {
			if lmm, ok := req.Model.(*shellmodels.LLMAdapterModel); ok {
				return agents.WithNewModel(func() (minisweagent.Model, error) {
					return lmm.Clone(), nil
				})
			}
			return nil
		}(),
		agents.WithSessionID(req.SessionID),
	}
	var trajDirPath, trajFilePath string
	if req.TrajectoryBaseDir != "" {
		trajFilePath = filepath.Join(req.TrajectoryBaseDir, "trajectory.json")
		trajDirPath = req.TrajectoryBaseDir
		opts = append(opts, agents.WithCheckpointDir(trajDirPath))
	} else if trajPath, err := artifacts.ShellAgentTrajectoryPath(req.SessionID, req.PlannerTurn); err == nil {
		trajDirPath = filepath.Dir(trajPath)
		trajFilePath = trajPath
		opts = append(opts, agents.WithCheckpointDir(trajDirPath))
	}
	agent := agents.NewDefaultAgent(req.Model, req.Env, req.Config, req.Prompts, opts...)

	// Set the interrupt step for deterministic checkpointing.
	agent.SetInterruptStep(req.InterruptStep)

	// Convert llm.Message to minisweagent.Message.
	msgs := convertMessages(req.PreconstructedMessages)

	// Resume detection: check for a prior session state file and either
	// resume from it or start a fresh run.
	var exitStatus, answer string
	var runErr error

	resumeAttempt := req.ResumeAttempt
	if !resumeAttempt {
		exitStatus, answer, runErr = agent.RunWithMessages(ctx, msgs)
	} else {
		if trajFilePath != "" {
			if loadedTraj, loadErr := run.LoadTrajectory(trajFilePath); loadErr == nil {
				currentSystem, hasCurrentSystem := firstSystemMessage(msgs)
				if loadedTraj.ResumeState != nil {
					agent.RestoreResumeState(loadedTraj.ResumeState)
					if hasCurrentSystem {
						agent.RunConfig.SystemPrompt = currentSystem.Content
						agent.RunConfig.SystemPromptCached = true
					}
					agent.SetInterruptStep(req.InterruptStep)
					if req.ExtraInstructions != "" && req.WarningWriter != nil {
						savedPrompt := loadedTraj.ResumeState.SystemPrompt
						if savedPrompt != "" && !strings.Contains(savedPrompt, req.ExtraInstructions) {
							fmt.Fprintf(req.WarningWriter, "mode mismatch: the saved system prompt differs from the current extra instructions")
						}
					}
				}
				if len(loadedTraj.Messages) > 0 {
					resumeMessages := replaceSystemMessage(loadedTraj.Messages, currentSystem, hasCurrentSystem)
					exitStatus, answer, runErr = agent.ResumeWithMessages(ctx, resumeMessages)
				} else {
					exitStatus, answer, runErr = agent.ResumeWithMessages(ctx, msgs)
				}
			} else if os.IsNotExist(loadErr) || errors.Is(loadErr, os.ErrNotExist) {
				exitStatus, answer, runErr = agent.RunWithMessages(ctx, msgs)
			} else {
				log.Printf("resume load error: %v, falling back to fresh run", loadErr)
				exitStatus, answer, runErr = agent.RunWithMessages(ctx, msgs)
			}
		} else {
			exitStatus, answer, runErr = agent.RunWithMessages(ctx, msgs)
		}
	}

	if runErr != nil {
		res.Error = runErr
	}
	res.ExitStatus = exitStatus
	res.Answer = answer

	// Build trajectory from final agent state for fresh and fallback cases.
	if res.Trajectory.Messages == nil {
		traj := run.FromAgent(agent, exitStatus, answer, nil)
		res.Trajectory = minisweagent.Trajectory{
			Messages:   traj.Messages,
			ExitStatus: traj.ExitStatus,
			Result:     traj.Result,
			ExtraInfo:  traj.ExtraInfo,
		}
	}

	// After the run, save the resume state file for future resumes,
	// but only when ResumeAttempt is true (do not persist state for
	// forced-fresh runs).
	if resumeAttempt {
		saveTraj := run.FromAgent(agent, exitStatus, answer, nil)
		if agent.RunConfig.CheckpointDir != "" {
			if saveErr := run.SaveTrajectoryToPath(saveTraj, agent.RunConfig.CheckpointDir); saveErr != nil {
				log.Printf("save final trajectory: %v", saveErr)
			}
		}
	}

	res.TrajectoryPath = trajFilePath
	if res.TrajectoryPath == "" {
		res.TrajectoryPath = trajDirPath
	}
	return res, nil
}

// convertMessages converts llm.Message values to minisweagent.Message.
func convertMessages(in []llm.Message) []minisweagent.Message {
	out := make([]minisweagent.Message, len(in))
	for i, m := range in {
		out[i] = minisweagent.Message{
			Role:     m.Role,
			Content:  m.Content,
			Metadata: m.Metadata,
		}
	}
	return out
}

func firstSystemMessage(messages []minisweagent.Message) (minisweagent.Message, bool) {
	for _, message := range messages {
		if message.Role == "system" {
			return message, true
		}
	}
	return minisweagent.Message{}, false
}

func replaceSystemMessage(messages []minisweagent.Message, current minisweagent.Message, ok bool) []minisweagent.Message {
	if !ok {
		return messages
	}
	refreshed := append([]minisweagent.Message(nil), messages...)
	for i := range refreshed {
		if refreshed[i].Role == "system" {
			refreshed[i] = current
			return refreshed
		}
	}
	return append([]minisweagent.Message{current}, refreshed...)
}

// RenderSystemPrompt renders the shell-agent system prompt using the
// embedded template. It is provided as a convenience so callers can
// render the prompt once per session and include it in
// PreconstructedMessages.
//
// answerTag overrides the final-answer tag name baked into the
// rendered template. Empty input keeps the default of "answer" so
// existing callers continue to render <answer>...</answer>. The value
// is layered on top of the lowest-precedence defaults inside
// run.MergeVars, so any pre-existing AnswerTag in extraVars wins.
func RenderSystemPrompt(prompts *minisweagent.PromptsConfig, extraVars map[string]interface{}, answerTag string, commandTag string) (string, error) {
	if prompts == nil {
		prompts = &minisweagent.PromptsConfig{}
	}
	cfg := &minisweagent.ShellAgentConfig{}
	// Use the same resolution logic as NewDefaultAgent.
	plannerPrompts := prompts.Planner
	if plannerPrompts == nil {
		plannerPrompts = &minisweagent.PlannerPromptsConfig{}
	}
	if strings.TrimSpace(plannerPrompts.SystemTemplate) == "" {
		var err error
		plannerPrompts.SystemTemplate, err = run.TemplateWithFallback(plannerPrompts.SystemTemplate, "shell_agent.system_template")
		if err != nil {
			return "", fmt.Errorf("shell-agent system template: %w", err)
		}
	}
	shellPrompts := prompts.ShellAgent
	if shellPrompts == nil {
		shellPrompts = &minisweagent.ShellAgentPromptsConfig{}
	}
	if plannerPrompts.SystemTemplate == "" && shellPrompts.LightweightSystemTemplate != "" {
		plannerPrompts.SystemTemplate = shellPrompts.LightweightSystemTemplate
	}
	if strings.TrimSpace(shellPrompts.SystemTemplate) == "" {
		var err error
		shellPrompts.SystemTemplate, err = run.TemplateWithFallback(shellPrompts.SystemTemplate, "shell_agent.system_template")
		if err != nil {
			return "", fmt.Errorf("shell-agent system template: %w", err)
		}
	}

	tag := normaliseAnswerTag(answerTag)
	defaults := map[string]interface{}{
		"answer_tag":  tag,
		"command_tag": commandTag,
		"CommandTag":  commandTag,
		"AnswerTag":   tag,
	}
	if cwd := templateString(extraVars, "CWD"); cwd != "" {
		defaults["cwd"] = cwd
		defaults["CWD"] = cwd
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current working directory: %w", err)
		}
		defaults["cwd"] = filepath.Clean(cwd)
		defaults["CWD"] = filepath.Clean(cwd)
	}
	merged := run.MergeVars(defaults, run.FlattenConfig(cfg), extraVars)
	return run.RenderTemplate(plannerPrompts.SystemTemplate, merged)
}

// RenderInstancePrompt renders the shell-agent instance prompt using the
// embedded template and the provided task description.
//
// cfg and env supply template variables (StepLimit, Machine, etc.) that
// the instance template requires. Either may be nil; missing values
// default to zero/empty.
//
// answerTag overrides the final-answer tag name baked into the
// rendered template. Empty input keeps the default of "answer" so
// existing callers continue to render <answer>...</answer>.
func RenderInstancePrompt(prompts *minisweagent.PromptsConfig, task string, cfg *minisweagent.ShellAgentConfig, env minisweagent.Environment, extraVars map[string]interface{}, answerTag string, commandTag string) (string, error) {
	shellPrompts := prompts.ShellAgent
	if shellPrompts == nil {
		shellPrompts = &minisweagent.ShellAgentPromptsConfig{}
	}
	if strings.TrimSpace(shellPrompts.InstanceTemplate) == "" {
		var err error
		shellPrompts.InstanceTemplate, err = run.TemplateWithFallback(shellPrompts.InstanceTemplate, "shell_agent.instance_template")
		if err != nil {
			return "", fmt.Errorf("shell-agent instance template: %w", err)
		}
	}

	tag := normaliseAnswerTag(answerTag)
	vars := map[string]interface{}{
		"Task":        task,
		"task":        task,
		"ShowFewShot": false,
		"answer_tag":  tag,
		"AnswerTag":   tag,
		"command_tag": commandTag,
		"CommandTag":  commandTag,
	}

	// Populate config-driven template vars.
	if cfg != nil {
		vars["StepLimit"] = cfg.MaxSteps
		vars["MaxSteps"] = cfg.MaxSteps
	}

	// Populate environment-driven template vars.
	if env != nil {
		for k, v := range env.GetTemplateVars() {
			vars[k] = v
		}
	}

	merged := run.MergeVars(vars, extraVars)
	return run.RenderTemplate(shellPrompts.InstanceTemplate, merged)
}

// NewModel creates a shell-agent Model adapter from the global
// configuration. It is the public factory for callers outside the
// shell-agent internal tree.
func NewModel(cfg *minisweagent.ModelConfig, apiKeyOverrides map[string]string) (minisweagent.Model, error) {
	return shellmodels.NewLLMAdapterModel(cfg, apiKeyOverrides)
}

// NewEnvironment creates a shell-agent Environment from the global
// configuration. It is the public factory for callers outside the
// shell-agent internal tree.
func NewEnvironment(cfg *minisweagent.EnvironmentConfig, persistTmpData bool) (minisweagent.Environment, error) {
	return environments.NewEnvironment(cfg, environments.WithPersistTmpData(persistTmpData))
}

// BuildShellAgentMessages constructs the pre-built message prefix for the
// shell-agent from the planner's conversation and the shell-agent config.
// It returns the system prompt followed by the planner's work messages.
//
// answerTag overrides the final-answer tag name baked into the system
// prompt; empty input keeps the default of "answer".
func BuildShellAgentMessages(convMessages []llm.Message, prompts *minisweagent.PromptsConfig, extraInstructions string, cwd string, answerTag string, commandTag string) ([]llm.Message, error) {
	templateVars := map[string]interface{}{}
	if trimmed := strings.TrimSpace(cwd); trimmed != "" {
		templateVars["cwd"] = trimmed
		templateVars["CWD"] = trimmed
	}
	sysPrompt, err := RenderSystemPrompt(prompts, templateVars, answerTag, commandTag)
	if err != nil {
		return nil, fmt.Errorf("render system prompt: %w", err)
	}

	if strings.TrimSpace(extraInstructions) != "" {
		sysPrompt = sysPrompt + "\n" + extraInstructions
	}

	out := make([]llm.Message, 0, len(convMessages)+1)
	out = append(out, llm.Message{Role: "system", Content: sysPrompt})
	out = append(out, convMessages...)
	return out, nil
}

// FewShotShellAgentExamples returns a short set of valid shell-agent
// responses to anchor the model on the expected format.  Each example
// consists of a reasoning sentence followed by a tag block using the
// supplied commandTag.  These are injected into the system prompt when
// the few-shot variant is "system" and only during the first few turns.
func FewShotShellAgentExamples(commandTag string) string {
	if commandTag == "" {
		commandTag = "command"
	}
	return fmt.Sprintf(`Examples of valid responses:

I will list the files in the current directory to understand the project layout.
<%s>
ls -la</%s>

Let me check the current state of the git repository to see which files have been modified.
<%s>
git status --short</%s>

I will read the build log to look for compiler errors or warnings.
<%s>
cat build.log</%s>`, commandTag, commandTag, commandTag, commandTag, commandTag, commandTag)
}

// BuildLibrary constructs a runner.ShellAgentLibrary from the global
// configuration. It creates the model adapter and environment once so
// they can be reused across turns.
//
// This is a convenience wrapper that combines NewModel,
// EnsureEnvironmentImage, and NewEnvironment into a single call.
//
// If modelAlias is non-empty, it is used as the model to resolve
// (e.g. from --model CLI flag or mode override). When empty,
// the global config's default_model is used.
//
// answerTag overrides the final-answer tag name baked into the
// shell-agent prompt templates (system/instance). Empty input keeps
// the default of "answer".
func BuildLibrary(global *llm.Config, apiKeyOverrides map[string]string, persistTmpData bool, modelAlias string, answerTag string, commandTag string) (*ShellAgentLibrary, error) {
	modelCfg := &minisweagent.ModelConfig{
		ModelName: modelAlias,
	}
	if modelAlias == "" {
		modelCfg.ModelName = global.DefaultModel
	}
	if strings.TrimSpace(modelCfg.APIKey) == "" {
		if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
			modelCfg.APIKey = key
		} else if key := os.Getenv("OPENAI_API_KEY"); key != "" {
			modelCfg.APIKey = key
		}
	}

	model, err := NewModel(modelCfg, apiKeyOverrides)
	if err != nil {
		return nil, fmt.Errorf("configure shell-agent model: %w", err)
	}

	env, err := NewEnvironment(global.Environment, persistTmpData)
	if global.Prompts == nil {
		global.Prompts = new(minisweagent.PromptsConfig)
	}
	if global.ShellAgent == nil {
		global.ShellAgent = new(minisweagent.ShellAgentConfig)
	}
	if err != nil {
		return nil, fmt.Errorf("configure shell-agent environment: %w", err)
	}

	fewShotVariant := "instance"
	if v := os.Getenv("MACHTIANI_SHELL_AGENT_FEW_SHOT_VARIANT"); v != "" {
		fewShotVariant = v
	}

	// EnforceEarlyCommands can be flipped per-invocation via the
	// MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS env var so A/B tests
	// (and ad-hoc sessions) can opt out without code changes.
	// EnforceEarlyCommands defaults to true. Setting
	// MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS to a falsy value
	// (0, false, no, off, case-insensitive) disables enforcement. Truthy
	// values (1, true, yes, on) keep it enabled. Any other value leaves
	// the flag at its default of true.
	enforceEarlyCommands := true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS"))); v != "" {
		switch v {
		case "0", "false", "no", "off":
			enforceEarlyCommands = false
		case "1", "true", "yes", "on":
			enforceEarlyCommands = true
		}
	}

	return &ShellAgentLibrary{
		Model:                model,
		Env:                  env,
		Config:               global.ShellAgent,
		Prompts:              global.Prompts,
		FewShotVariant:       fewShotVariant,
		EnforceEarlyCommands: enforceEarlyCommands,
		AnswerTag:            normaliseAnswerTag(answerTag),
		CommandTag:           normaliseCommandTag(commandTag),
		CWD:                  templateString(env.GetTemplateVars(), "CWD"),
	}, nil
}

func templateString(vars map[string]interface{}, key string) string {
	if value, ok := vars[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

// ShellAgentLibrary holds the live objects needed for the in-process
// shell-agent library path. It is constructed once and reused across
// turns.
type ShellAgentLibrary struct {
	Model             minisweagent.Model
	Env               minisweagent.Environment
	Config            *minisweagent.ShellAgentConfig
	Prompts           *minisweagent.PromptsConfig
	ExtraInstructions string
	CWD               string

	// FewShotVariant controls the few-shot injection variant.
	// Default is "instance". Valid values are: "off", "none", "system", "instance".
	FewShotVariant string

	// EnforceEarlyCommands opts the library into stricter
	// <command>...</command> emission enforcement on the first few
	// planner turns. When true, shell-agent.Run receives
	// EnforceEarlyCommands=true on every work_request and applies
	// turn-specific behaviour. The flag defaults to true; set
	// MACHTIANI_SHELL_AGENT_ENFORCE_EARLY_COMMANDS=false to disable.
	EnforceEarlyCommands bool

	// AnswerTag is the final-answer tag name baked into the
	// shell-agent prompt templates. The default is "answer"; callers
	// can override it via the --answer-tag CLI flag. The library
	// surface propagates this value into both RenderSystemPrompt and
	// RenderInstancePrompt so the parser and the prompt renderer
	// stay in sync.
	AnswerTag string

	// CommandTag overrides the command tag name used by the parser
	// and prompt templates. Defaults to "command".
	CommandTag string
}

// ValidateAnswerTag checks the user-supplied answer-tag name against
// the rules in PlanAnswerTag.md (rule 12). The name must be
// non-empty and must not contain characters that would either break
// the prompt template (e.g. "{{" / "}}") or collide with the XML
// bracket syntax the parser uses ("<", ">", "/").
//
// The empty string is accepted and normalised to "answer" downstream;
// callers that want to reject the empty string explicitly should
// compare against "" before calling.
func ValidateAnswerTag(tag string) error {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return nil
	}
	for _, banned := range []string{"<", ">", "/", "{{", "}}"} {
		if strings.Contains(trimmed, banned) {
			return fmt.Errorf("invalid --answer-tag %q: must not contain %q", tag, banned)
		}
	}
	return nil
}

// normaliseAnswerTag returns the canonical tag name: empty /
// whitespace input becomes "answer" so the parser and template
// renderer always have a non-empty value. Callers should validate
// the user-supplied tag with ValidateAnswerTag before passing it in.
func normaliseAnswerTag(tag string) string {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return "answer"
	}
	return trimmed
}

// normaliseCommandTag returns the canonical command tag name: empty /
// whitespace input becomes "command" so the parser and template
// renderer always have a non-empty value.
func normaliseCommandTag(tag string) string {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return "command"
	}
	return trimmed
}

// ComposeEffectiveCommandTag returns the canonical command tag for
// the supplied tagSuffix: "command" when tagSuffix is empty, or
// "command-" followed by tagSuffix otherwise. The result is not
// whitespace-trimmed or validated here — callers are expected to
// supply a clean suffix.
func ComposeEffectiveCommandTag(tagSuffix string) string {
	if tagSuffix == "" {
		return "command"
	}
	return "command-" + tagSuffix
}

// ComposeEffectiveTags resolves the effective answer and command tags
// from an explicit answerTag and a tagSuffix. Mutual exclusion is
// enforced: if both are non-empty the function returns empty strings
// and an error. When tagSuffix is non-empty it composes both tags
// from the suffix; otherwise it normalises answerTag and falls back
// to the default command tag.
func ComposeEffectiveTags(answerTag string, tagSuffix string) (string, string, error) {
	if answerTag != "" && tagSuffix != "" {
		return "", "", fmt.Errorf("cannot use both --answer-tag and --tag flags together")
	}
	if tagSuffix != "" {
		return "answer-" + tagSuffix, ComposeEffectiveCommandTag(tagSuffix), nil
	}
	return normaliseAnswerTag(answerTag), normaliseCommandTag(""), nil
}

// ComposeEffectiveAnswerTag combines an explicit answer-tag and a
// tag-suffix into a single effective answer tag, validates it, and
// returns the result. At most one of answerTag / tagSuffix may be
// non-empty.
func ComposeEffectiveAnswerTag(answerTag, tagSuffix string) (string, error) {
	if tagSuffix != "" && answerTag != "" {
		return "", fmt.Errorf("cannot set both --tag and --answer-tag")
	}
	effective := answerTag
	if tagSuffix != "" {
		effective = "answer-" + tagSuffix
	}
	if err := ValidateAnswerTag(effective); err != nil {
		return "", err
	}
	return effective, nil
}
