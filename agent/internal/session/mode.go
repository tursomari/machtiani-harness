package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/ui"
	"github.com/tursomari/machtiani/agent/internal/conversation"
)

const modePlanFilename = "mode-plan.json"

// modePlanState captures the persisted plan for a mode system session.
type modePlanState struct {
	Goal              string          `json:"goal"`
	Mode              string          `json:"mode"`
	InstructionPath   string          `json:"instruction_path,omitempty"`
	InstructionFormat string          `json:"instruction_format,omitempty"`
	Tasks             []modeTaskState `json:"tasks"`
	LastUpdated       time.Time       `json:"last_updated"`
}

// modeTaskState tracks execution state for an individual task.
type modeTaskState struct {
	Step           int    `json:"step,omitempty"`
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	Instruction    string `json:"goal,omitempty"`
	PlannerOverlay string `json:"planner_overlay,omitempty"`
	UserGuidance   string `json:"user_guidance,omitempty"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	Attempts       int    `json:"attempts"`
	Summary        string `json:"summary,omitempty"`
}

type modeContext struct {
	SessionID       string
	Goal            string
	ResumePrompt    string
	Config          legacyConfig
	Options         Options
	Display         ui.SessionDisplay
	InstructionPath string
	Instruction     llm.ModeInstructions
	DiagWriter      io.Writer
}

// modeConfigResult captures the configuration applied by applyMode()
// when it returns handled=false so the caller can access the updated plan.
type modeConfigResult struct {
	Plan modePlanState
}

// applyMode is a pre-loop configuration step. It loads (or creates) the
// mode-plan, applies the task's PlannerOverlay and mode defaults to the
// current session's configuration, and returns handled=false so that
// runSession()'s existing planner loop runs with the configured state.
//
// It returns handled=true only for error conditions. For all normal paths
// (fresh run, resume of running/suspended task, follow-up after completion)
// it configures and returns handled=false.
func applyMode(ctx *modeContext) (modeConfigResult, bool) {
	mode := strings.TrimSpace(ctx.Config.mode)
	if mode == "" {
		return modeConfigResult{}, false
	}

	plan, err := loadOrCreateModePlan(ctx.SessionID, ctx.Goal, mode, ctx.InstructionPath, ctx.Instruction)
	if err != nil {
		fmt.Fprintf(ctx.DiagWriter, "Error loading mode plan: %v\n", err)
		return modeConfigResult{Plan: plan}, true
	}

	ctx.Display.RenderModePlan(tasksToDisplay(plan.Tasks))

	// Follow-up after completion: all tasks done and user provided new input.
	// Return handled=false so the planner loop picks up the new prompt with
	// the full prior conversation already in place.
	if allTasksComplete(plan) && strings.TrimSpace(ctx.ResumePrompt) != "" {
		return modeConfigResult{Plan: plan}, false
	}

	// Configure the session for the single task.
	updatedPlan, err := configureModePlan(ctx, plan)
	if err != nil {
		fmt.Fprintf(ctx.DiagWriter, "Error configuring mode plan: %v\n", err)
		return modeConfigResult{Plan: updatedPlan}, true
	}

	// Return handled=false so runSession() continues into the planner loop.
	return modeConfigResult{Plan: updatedPlan}, false
}

// configureModePlan applies the single task's configuration to the current
// session: PlannerOverlay, mode defaults, task overrides, and status tracking.
// It does NOT spawn a child session.
func configureModePlan(ctx *modeContext, plan modePlanState) (modePlanState, error) {
	if len(plan.Tasks) == 0 {
		return plan, fmt.Errorf("mode plan has no tasks")
	}

	task := &plan.Tasks[0]

	// If suspended, reset to running (the user is resuming the session).
	if strings.EqualFold(strings.TrimSpace(task.Status), "suspended") {
		task.Status = "running"
	}

	// If already complete (and no resume prompt, since follow-up is handled
	// above), return the plan as-is — the summary path in applyMode
	// was removed; the planner loop handles finalization.
	if strings.EqualFold(strings.TrimSpace(task.Status), "complete") {
		return plan, nil
	}

	// Apply PlannerOverlay to the session's planner config.
	if overlay := strings.TrimSpace(task.PlannerOverlay); overlay != "" {
		ctx.Options.PlannerOverlay = overlay
	}

	// Apply mode defaults (e.g., patch=true, maxTurns floor for coding mode).
	applyModeDefaults(&ctx.Options.Config, task.Mode)


	// Mark the task as running.
	task.Status = "running"

	if err := persistModePlan(ctx.SessionID, plan); err != nil {
		return plan, err
	}

	if ctx.Display != nil {
		ctx.Display.UpdateModeTaskStatus(0, task.Title, "running")
	}

	return plan, nil
}

// CompleteModePlanTask marks the first non-complete task in the mode-plan as
// complete. Called from the finalization path in runSession() after the
// planner loop finishes.
func CompleteModePlanTask(sessionID string) error {
	planPath, err := modePlanPath(sessionID)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(planPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no mode-plan, nothing to update
		}
		return fmt.Errorf("read mode plan: %w", err)
	}
	var plan modePlanState
	if err := json.Unmarshal(data, &plan); err != nil {
		return fmt.Errorf("decode mode plan: %w", err)
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
	return persistModePlan(sessionID, plan)
}

func loadOrCreateModePlan(sessionID, goal, mode, instructionPath string, instructions llm.ModeInstructions) (modePlanState, error) {
	planPath, err := modePlanPath(sessionID)
	if err != nil {
		return modePlanState{}, err
	}
	data, err := os.ReadFile(planPath)
	if err == nil {
		var plan modePlanState
		if uErr := json.Unmarshal(data, &plan); uErr != nil {
			return modePlanState{}, fmt.Errorf("decode mode plan: %w", uErr)
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
		return modePlanState{}, fmt.Errorf("read mode plan: %w", err)
	}

	plan := modePlanState{
		Goal:              goal,
		Mode:              mode,
		InstructionPath:   resolvedInstructionPath("", instructionPath, instructions.Path),
		InstructionFormat: string(instructions.Format),
		Tasks:             instructionsToTasks(goal, mode, instructions),
		LastUpdated:       time.Now().UTC(),
	}
	if perr := persistModePlan(sessionID, plan); perr != nil {
		return modePlanState{}, perr
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

func instructionsToTasks(goal, mode string, instructions llm.ModeInstructions) []modeTaskState {
	switch instructions.Format {
	case llm.ModeInstructionsFormatTOML:
		return tasksFromTOML(goal, mode, instructions)
	case llm.ModeInstructionsFormatText, "":
		return tasksFromText(goal, mode, instructions.Raw)
	default:
		return tasksFromText(goal, mode, instructions.Raw)
	}
}

func tasksFromText(goal, mode, instructions string) []modeTaskState {
	var tasks []modeTaskState
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
		tasks = append(tasks, modeTaskState{
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

func tasksFromTOML(goal, mode string, instructions llm.ModeInstructions) []modeTaskState {
	task := instructions.Task
	if strings.TrimSpace(task.Title) == "" {
		return defaultTasksForMode(goal, mode)
	}
	title := strings.TrimSpace(task.Title)
	description := strings.TrimSpace(task.Description)
	instruction := taskInstructionText(title, task.Instruction)
	plannerOverlay := taskPlannerOverlayText(task.SystemPrompt)
	return []modeTaskState{
		{
			Step:           1,
			Title:          title,
			Description:    description,
			Instruction:    instruction,
			PlannerOverlay: plannerOverlay,
			Mode:           mode,
			Status:         "pending",

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

func defaultTasksForMode(goal, mode string) []modeTaskState {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "coding":
		return []modeTaskState{
			{
				Title:       "Implement solution",
				Instruction: fmt.Sprintf("Implement changes to address: %s", goal),
				Mode:        "coding",
				Status:      "pending",
			},
		}
	case "research":
		return []modeTaskState{
			{
				Title:       "Synthesize findings",
				Instruction: fmt.Sprintf("Synthesize findings addressing: %s", goal),
				Mode:        "research",
				Status:      "pending",
			},
		}
	default:
		return []modeTaskState{
			{
				Title:       "Execute plan",
				Instruction: fmt.Sprintf("Execute and document progress for: %s", goal),
				Mode:        "other",
				Status:      "pending",
			},
		}
	}
}

func persistModePlan(sessionID string, plan modePlanState) error {
	planPath, err := modePlanPath(sessionID)
	if err != nil {
		return err
	}
	plan.LastUpdated = time.Now().UTC()
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("encode mode plan: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		return fmt.Errorf("ensure mode plan dir: %w", err)
	}
	if err := os.WriteFile(planPath, data, 0o644); err != nil {
		return fmt.Errorf("write mode plan: %w", err)
	}
	return nil
}

func UpdateModePlanProgress(sessionID string, progress *conversation.PlannerProgressState) error {
	// PlannerProgress is now stored only in conversation.json; mode-plan.json
	// no longer carries a redundant copy. Kept as a no-op to preserve the
	// call-site surface across the codebase.
	_ = sessionID
	_ = progress
	return nil
}

func modePlanPath(sessionID string) (string, error) {
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, modePlanFilename), nil
}

func tasksToDisplay(tasks []modeTaskState) []ui.ModeTaskDisplay {
	displays := make([]ui.ModeTaskDisplay, 0, len(tasks))
	for idx, task := range tasks {
		displays = append(displays, ui.ModeTaskDisplay{
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
		if cfg.MaxTurns < 3 {
			cfg.MaxTurns = 3
		}
	}
}





func modesFromPlan(plan modePlanState) []string {
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

func allTasksComplete(plan modePlanState) bool {
	for _, task := range plan.Tasks {
		if strings.ToLower(strings.TrimSpace(task.Status)) != "complete" {
			return false
		}
	}
	return len(plan.Tasks) > 0
}
