package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

const metaPlanFilename = "meta-plan.json"

// metaPlanState captures the persisted plan for a meta-orchestrated session.
type metaPlanState struct {
	Goal              string          `json:"goal"`
	Mode              string          `json:"mode"`
	InstructionPath   string          `json:"instruction_path,omitempty"`
	InstructionFormat string          `json:"instruction_format,omitempty"`
	Tasks             []metaTaskState `json:"tasks"`
	LastUpdated       time.Time       `json:"last_updated"`
}

// metaTaskState tracks execution state for an individual task.
type metaTaskState struct {
	Step           int    `json:"step,omitempty"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	Instruction    string `json:"goal,omitempty"`
	PlannerOverlay string `json:"planner_overlay,omitempty"`
	UserGuidance   string `json:"user_guidance,omitempty"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	ShellAgent     *bool  `json:"shell_agent,omitempty"`
	PatchMode      *bool  `json:"patch_mode,omitempty"`
	Attempts       int    `json:"attempts"`
	Summary        string `json:"summary,omitempty"`
}

type metaContext struct {
	SessionID       string
	Goal            string
	ResumePrompt    string
	Config          legacyConfig
	Options         Options
	Display         *ui.TerminalDisplay
	InstructionPath string
	Instruction     llm.MetaInstructions
}

// metaConfigResult captures the configuration applied by metaOrchestrate()
// when it returns handled=false so the caller can access the updated plan.
type metaConfigResult struct {
	Plan metaPlanState
}

// metaOrchestrate is a pre-loop configuration step. It loads (or creates) the
// meta-plan, applies the task's PlannerOverlay and mode defaults to the
// current session's configuration, and returns handled=false so that
// runSession()'s existing planner loop runs with the configured state.
//
// It returns handled=true only for error conditions. For all normal paths
// (fresh run, resume of running/suspended task, follow-up after completion)
// it configures and returns handled=false.
func metaOrchestrate(ctx *metaContext) (metaConfigResult, bool) {
	mode := strings.TrimSpace(ctx.Config.mode)
	if mode == "" {
		return metaConfigResult{}, false
	}

	plan, err := loadOrCreateMetaPlan(ctx.SessionID, ctx.Goal, mode, ctx.InstructionPath, ctx.Instruction)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading meta plan: %v\n", err)
		return metaConfigResult{Plan: plan}, true
	}

	ctx.Display.RenderMetaPlan(tasksToDisplay(plan.Tasks))

	// Follow-up after completion: all tasks done and user provided new input.
	// Return handled=false so the planner loop picks up the new prompt with
	// the full prior conversation already in place.
	if allTasksComplete(plan) && strings.TrimSpace(ctx.ResumePrompt) != "" {
		return metaConfigResult{Plan: plan}, false
	}

	// Configure the session for the single task.
	updatedPlan, err := configureMetaPlan(ctx, plan)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error configuring meta plan: %v\n", err)
		return metaConfigResult{Plan: updatedPlan}, true
	}

	// Return handled=false so runSession() continues into the planner loop.
	return metaConfigResult{Plan: updatedPlan}, false
}

// configureMetaPlan applies the single task's configuration to the current
// session: PlannerOverlay, mode defaults, task overrides, and status tracking.
// It does NOT spawn a child session.
func configureMetaPlan(ctx *metaContext, plan metaPlanState) (metaPlanState, error) {
	if len(plan.Tasks) == 0 {
		return plan, fmt.Errorf("meta plan has no tasks")
	}

	task := &plan.Tasks[0]

	// If suspended, reset to running (the user is resuming the session).
	if strings.EqualFold(strings.TrimSpace(task.Status), "suspended") {
		task.Status = "running"
	}

	// If already complete (and no resume prompt, since follow-up is handled
	// above), return the plan as-is — the summary path in metaOrchestrate
	// was removed; the planner loop handles finalization.
	if strings.EqualFold(strings.TrimSpace(task.Status), "complete") {
		return plan, nil
	}

	// Apply PlannerOverlay to the session's planner config.
	if overlay := strings.TrimSpace(task.PlannerOverlay); overlay != "" {
		ctx.Options.PlannerOverlay = overlay
	}

	// Apply mode defaults (e.g., patch=true, maxSteps floor for coding mode).
	applyModeDefaults(&ctx.Options.Config, task.Mode)

	// Apply task overrides (shell_agent, patch_mode from TOML).
	applyTaskOverrides(&ctx.Options, *task)

	// Mark the task as running.
	task.Status = "running"

	if err := persistMetaPlan(ctx.SessionID, plan); err != nil {
		return plan, err
	}

	if ctx.Display != nil {
		ctx.Display.UpdateMetaTaskStatus(0, task.Title, "running")
	}

	return plan, nil
}

// CompleteMetaPlanTask marks the first non-complete task in the meta-plan as
// complete. Called from the finalization path in runSession() after the
// planner loop finishes.
func CompleteMetaPlanTask(sessionID string) error {
	planPath, err := metaPlanPath(sessionID)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no meta-plan, nothing to update
		}
		return fmt.Errorf("read meta plan: %w", err)
	}
	var plan metaPlanState
	if err := json.Unmarshal(data, &plan); err != nil {
		return fmt.Errorf("decode meta plan: %w", err)
	}
	changed := false
	for i := range plan.Tasks {
		status := strings.ToLower(strings.TrimSpace(plan.Tasks[i].Status))
		if status != "complete" {
			plan.Tasks[i].Status = "complete"
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	return persistMetaPlan(sessionID, plan)
}

func loadOrCreateMetaPlan(sessionID, goal, mode, instructionPath string, instructions llm.MetaInstructions) (metaPlanState, error) {
	planPath, err := metaPlanPath(sessionID)
	if err != nil {
		return metaPlanState{}, err
	}
	data, err := os.ReadFile(planPath)
	if err == nil {
		var plan metaPlanState
		if uErr := json.Unmarshal(data, &plan); uErr != nil {
			return metaPlanState{}, fmt.Errorf("decode meta plan: %w", uErr)
		}
		plan.InstructionPath = resolvedInstructionPath(plan.InstructionPath, instructionPath, instructions.Path)
		if instructions.Format != "" {
			plan.InstructionFormat = string(instructions.Format)
		}
		if len(plan.Tasks) == 0 {
			plan.Tasks = instructionsToTasks(goal, mode, instructions)
		}
		return plan, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return metaPlanState{}, fmt.Errorf("read meta plan: %w", err)
	}

	plan := metaPlanState{
		Goal:              goal,
		Mode:              mode,
		InstructionPath:   resolvedInstructionPath("", instructionPath, instructions.Path),
		InstructionFormat: string(instructions.Format),
		Tasks:             instructionsToTasks(goal, mode, instructions),
		LastUpdated:       time.Now().UTC(),
	}
	if perr := persistMetaPlan(sessionID, plan); perr != nil {
		return metaPlanState{}, perr
	}
	return plan, nil
}

func resolvedInstructionPath(existing, provided, docPath string) string {
	if path := strings.TrimSpace(docPath); path != "" {
		return path
	}
	if path := strings.TrimSpace(provided); path != "" {
		return path
	}
	return existing
}

func instructionsToTasks(goal, mode string, instructions llm.MetaInstructions) []metaTaskState {
	switch instructions.Format {
	case llm.MetaInstructionsFormatTOML:
		return tasksFromTOML(goal, mode, instructions)
	case llm.MetaInstructionsFormatText, "":
		return tasksFromText(goal, mode, instructions.Raw)
	default:
		return tasksFromText(goal, mode, instructions.Raw)
	}
}

func tasksFromText(goal, mode, instructions string) []metaTaskState {
	var tasks []metaTaskState
	lines := strings.Split(instructions, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		}
		if trimmed == "" {
			continue
		}
		step := len(tasks) + 1
		tasks = append(tasks, metaTaskState{
			Step:        step,
			Title:       trimmed,
			Instruction: trimmed,
			Mode:        mode,
			Status:      "pending",
		})
		if len(tasks) == 5 {
			break
		}
	}
	if len(tasks) >= 2 {
		return tasks
	}
	return defaultTasksForMode(goal, mode)
}

func tasksFromTOML(goal, mode string, instructions llm.MetaInstructions) []metaTaskState {
	task := instructions.Task
	if strings.TrimSpace(task.Title) == "" {
		return defaultTasksForMode(goal, mode)
	}
	title := strings.TrimSpace(task.Title)
	description := strings.TrimSpace(task.Description)
	instruction := taskInstructionText(title, task.Instruction)
	plannerOverlay := taskPlannerOverlayText(task.SystemPrompt)
	return []metaTaskState{
		{
			Step:           1,
			Title:          title,
			Description:    description,
			Instruction:    instruction,
			PlannerOverlay: plannerOverlay,
			Mode:           mode,
			Status:         "pending",
			ShellAgent:     task.ShellAgent,
			PatchMode:      task.PatchMode,
		},
	}
}

func taskInstructionText(title, instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction != "" {
		return instruction
	}
	return strings.TrimSpace(title)
}

func taskPlannerOverlayText(systemPrompt string) string {
	return strings.TrimSpace(systemPrompt)
}

func defaultTasksForMode(goal, mode string) []metaTaskState {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "coding":
		return []metaTaskState{
			{
				Title:       "Implement solution",
				Instruction: fmt.Sprintf("Implement changes to address: %s", goal),
				Mode:        "coding",
				Status:      "pending",
			},
		}
	case "research":
		return []metaTaskState{
			{
				Title:       "Synthesize findings",
				Instruction: fmt.Sprintf("Synthesize findings addressing: %s", goal),
				Mode:        "research",
				Status:      "pending",
			},
		}
	default:
		return []metaTaskState{
			{
				Title:       "Execute plan",
				Instruction: fmt.Sprintf("Execute and document progress for: %s", goal),
				Mode:        "other",
				Status:      "pending",
			},
		}
	}
}

func persistMetaPlan(sessionID string, plan metaPlanState) error {
	planPath, err := metaPlanPath(sessionID)
	if err != nil {
		return err
	}
	plan.LastUpdated = time.Now().UTC()
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode meta plan: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		return fmt.Errorf("ensure meta plan dir: %w", err)
	}
	if err := os.WriteFile(planPath, data, 0o644); err != nil {
		return fmt.Errorf("write meta plan: %w", err)
	}
	return nil
}

func UpdateMetaPlanProgress(sessionID string, progress *PlannerProgressState) error {
	// PlannerProgress is now stored only in session-state.json; meta-plan.json
	// no longer carries a redundant copy. Kept as a no-op to preserve the
	// call-site surface across the codebase.
	_ = sessionID
	_ = progress
	return nil
}

func metaPlanPath(sessionID string) (string, error) {
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, metaPlanFilename), nil
}

func tasksToDisplay(tasks []metaTaskState) []ui.MetaTaskDisplay {
	displays := make([]ui.MetaTaskDisplay, 0, len(tasks))
	for idx, task := range tasks {
		displays = append(displays, ui.MetaTaskDisplay{
			Index:  idx + 1,
			Title:  task.Title,
			Mode:   task.Mode,
			Status: task.Status,
		})
	}
	return displays
}

func applyModeDefaults(cfg *Config, mode string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "coding":
		cfg.Patch = true
		if cfg.MaxSteps < 3 {
			cfg.MaxSteps = 3
		}
	}
}

func applyTaskOverrides(opts *Options, task metaTaskState) {
	if opts == nil {
		return
	}
	if task.ShellAgent != nil {
		opts.Config.ShellAgent = *task.ShellAgent
	}
	if task.PatchMode != nil {
		opts.Config.Patch = *task.PatchMode
		if !*task.PatchMode {
			opts.Config.PatchStrict = false
		}
	}
}

func boolLabel(flag *bool) string {
	if flag == nil {
		return "inherit"
	}
	if *flag {
		return "true"
	}
	return "false"
}

func metaModesFromPlan(plan metaPlanState) []string {
	seen := make(map[string]struct{})
	var modes []string
	for _, task := range plan.Tasks {
		mode := strings.ToLower(strings.TrimSpace(task.Mode))
		if mode == "" {
			continue
		}
		if _, ok := seen[mode]; ok {
			continue
		}
		seen[mode] = struct{}{}
		modes = append(modes, mode)
	}
	return modes
}

func allTasksComplete(plan metaPlanState) bool {
	for _, task := range plan.Tasks {
		if strings.ToLower(strings.TrimSpace(task.Status)) != "complete" {
			return false
		}
	}
	return len(plan.Tasks) > 0
}
