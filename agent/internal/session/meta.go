package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/parser"
	"github.com/tursomari/machtiani/agent/internal/transcript"
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
	Step        int    `json:"step,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Goal        string `json:"goal"`
	Mode        string `json:"mode"`
	Status      string `json:"status"`
	ShellAgent  bool   `json:"shell_agent"`
	PatchMode   bool   `json:"patch_mode"`
	SessionID   string `json:"session_id,omitempty"`
	Attempts    int    `json:"attempts"`
	Summary     string `json:"summary,omitempty"`
	Transcript  string `json:"transcript_path,omitempty"`
	FinalAnswer string `json:"final_answer_path,omitempty"`
}

type metaContext struct {
	RootCtx         context.Context
	SessionID       string
	Goal            string
	Config          legacyConfig
	Options         Options
	Display         *ui.TerminalDisplay
	InstructionPath string
	Instruction     llm.MetaInstructions
	Transcript      *transcript.Transcript
}

type metaOutcome struct {
	Plan        metaPlanState
	FinalAnswer string
	ExitCode    int
	Err         error
}

func metaOrchestrate(ctx metaContext) (metaOutcome, bool) {
	mode := strings.TrimSpace(ctx.Config.mode)
	if mode == "" {
		return metaOutcome{}, false
	}
	if strings.TrimSpace(ctx.Config.parentSessionID) != "" {
		return metaOutcome{}, false
	}

	plan, err := loadOrCreateMetaPlan(ctx.SessionID, ctx.Goal, mode, ctx.InstructionPath, ctx.Instruction)
	if err != nil {
		return metaOutcome{ExitCode: 1, Err: err}, true
	}

	ctx.Display.RenderMetaPlan(tasksToDisplay(plan.Tasks))

	updatedPlan, err := executeMetaPlan(ctx, plan)
	if err != nil {
		outcome := metaOutcome{Plan: updatedPlan, Err: err, ExitCode: 1}
		return outcome, true
	}

	summary := renderMetaSummary(ctx.Goal, updatedPlan)
	outcome := metaOutcome{Plan: updatedPlan, FinalAnswer: summary, ExitCode: 0}
	return outcome, true
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
		taskGoal := composeTaskGoal(strings.TrimSpace(goal), trimmed, "")
		step := len(tasks) + 1
		tasks = append(tasks, metaTaskState{
			Step:   step,
			Title:  trimmed,
			Goal:   taskGoal,
			Mode:   mode,
			Status: "pending",
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
	if len(instructions.Tasks) == 0 {
		return defaultTasksForMode(goal, mode)
	}
	items := append([]llm.MetaInstructionTask(nil), instructions.Tasks...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Step == items[j].Step {
			return i < j
		}
		return items[i].Step < items[j].Step
	})
	tasks := make([]metaTaskState, 0, len(items))
	baseGoal := strings.TrimSpace(goal)
	for idx, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			continue
		}
		description := strings.TrimSpace(item.Description)
		step := item.Step
		if step <= 0 {
			step = idx + 1
		}
		goalText := composeTaskGoal(baseGoal, title, description)
		tasks = append(tasks, metaTaskState{
			Step:        step,
			Title:       title,
			Description: description,
			Goal:        goalText,
			Mode:        mode,
			Status:      "pending",
			ShellAgent:  item.ShellAgent,
			PatchMode:   item.PatchMode,
		})
	}
	if len(tasks) == 0 {
		return defaultTasksForMode(goal, mode)
	}
	return tasks
}

func composeTaskGoal(sessionGoal, title, description string) string {
	sessionGoal = strings.TrimSpace(sessionGoal)
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	var sections []string
	if sessionGoal != "" {
		sections = append(sections, sessionGoal)
	}
	focus := title
	if focus == "" {
		focus = description
	}
	focus = strings.TrimSpace(focus)
	if focus != "" {
		sections = append(sections, fmt.Sprintf("Focus: %s", focus))
	}
	if description != "" {
		sections = append(sections, fmt.Sprintf("Task details: %s", description))
	}
	return strings.Join(sections, "\n\n")
}

func defaultTasksForMode(goal, mode string) []metaTaskState {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "coding":
		return []metaTaskState{
			{
				Title:  "Review existing context",
				Goal:   fmt.Sprintf("Assess current state relevant to: %s", goal),
				Mode:   "coding",
				Status: "pending",
			},
			{
				Title:  "Implement solution",
				Goal:   fmt.Sprintf("Implement changes to address: %s", goal),
				Mode:   "coding",
				Status: "pending",
			},
			{
				Title:  "Validate and summarize",
				Goal:   fmt.Sprintf("Validate updates and summarize results for: %s", goal),
				Mode:   "coding",
				Status: "pending",
			},
		}
	case "research":
		return []metaTaskState{
			{
				Title:  "Collect background",
				Goal:   fmt.Sprintf("Collect background information for: %s", goal),
				Mode:   "research",
				Status: "pending",
			},
			{
				Title:  "Synthesize findings",
				Goal:   fmt.Sprintf("Synthesize findings addressing: %s", goal),
				Mode:   "research",
				Status: "pending",
			},
		}
	default:
		return []metaTaskState{
			{
				Title:  "Plan approach",
				Goal:   fmt.Sprintf("Plan the steps required for: %s", goal),
				Mode:   "other",
				Status: "pending",
			},
			{
				Title:  "Execute plan",
				Goal:   fmt.Sprintf("Execute and document progress for: %s", goal),
				Mode:   "other",
				Status: "pending",
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

func metaPlanPath(sessionID string) (string, error) {
	dir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, metaPlanFilename), nil
}

func executeMetaPlan(ctx metaContext, plan metaPlanState) (metaPlanState, error) {
	tasks := append([]metaTaskState(nil), plan.Tasks...)
	metaTurn := 1000
	for idx := range tasks {
		task := &tasks[idx]
		if task.Status == "complete" {
			continue
		}
		startSummary := buildMetaStartSummary(ctx.Goal, *task, tasks[:idx])
		writeMetaTurn(ctx, metaTurn, fmt.Sprintf("Meta task start: %s", task.Title), startSummary, "meta-start")
		ctx.Display.UpdateMetaTaskStatus(idx, task.Title, "running", task.SessionID)
		priorAnswer := loadPreviousFinalAnswer(tasks[:idx])
		result, err := runMetaTask(ctx, *task, priorAnswer, idx)
		task.Attempts++
		if err != nil {
			task.Status = "failed"
			task.Summary = err.Error()
			task.Transcript = result.TranscriptPath
			task.FinalAnswer = result.FinalAnswerPath
			if result.SessionID != "" {
				task.SessionID = result.SessionID
			}
			endSummary := buildMetaEndSummary(*task)
			writeMetaTurn(ctx, metaTurn+1, fmt.Sprintf("Meta task result: %s", task.Title), endSummary, "meta-end")
			metaTurn += 2
			updated := plan
			updated.Tasks = tasks
			if perr := persistMetaPlan(ctx.SessionID, updated); perr != nil {
				return updated, perr
			}
			ctx.Display.UpdateMetaTaskStatus(idx, task.Title, "failed", task.SessionID)
			return updated, err
		}
		task.Status = "complete"
		task.SessionID = result.SessionID
		task.Summary = result.Summary
		task.Transcript = result.TranscriptPath
		task.FinalAnswer = result.FinalAnswerPath
		endSummary := buildMetaEndSummary(*task)
		writeMetaTurn(ctx, metaTurn+1, fmt.Sprintf("Meta task result: %s", task.Title), endSummary, "meta-end")
		metaTurn += 2
		updated := plan
		updated.Tasks = tasks
		if perr := persistMetaPlan(ctx.SessionID, updated); perr != nil {
			return updated, perr
		}
		ctx.Display.UpdateMetaTaskStatus(idx, task.Title, "complete", task.SessionID)
	}
	plan.Tasks = tasks
	if perr := persistMetaPlan(ctx.SessionID, plan); perr != nil {
		return plan, perr
	}
	return plan, nil
}

func writeMetaTurn(ctx metaContext, step int, question, summary, decision string) {
	if ctx.Config.dryRun || ctx.Transcript == nil {
		return
	}
	if err := ctx.Transcript.WriteTurn(step, question, "", nil, summary, decision); err != nil {
		fmt.Fprintf(os.Stderr, "Meta transcript write error: %v\n", err)
	}
}

func buildMetaStartSummary(goal string, task metaTaskState, prior []metaTaskState) string {
	var b strings.Builder
	trimmedGoal := strings.TrimSpace(goal)
	if trimmedGoal == "" {
		trimmedGoal = "(none)"
	}
	b.WriteString("Original Prompt:\n")
	b.WriteString(trimmedGoal)
	b.WriteString("\n\nCurrent Task:\n")
	b.WriteString(fmt.Sprintf("- Title: %s\n", task.Title))
	b.WriteString(fmt.Sprintf("- Mode: %s\n", strings.TrimSpace(task.Mode)))
	if task.Step > 0 {
		b.WriteString(fmt.Sprintf("- Step: %d\n", task.Step))
	}
	b.WriteString(fmt.Sprintf("- Shell Agent: %t\n", task.ShellAgent))
	b.WriteString(fmt.Sprintf("- Patch Mode: %t\n", task.PatchMode))
	if desc := strings.TrimSpace(task.Description); desc != "" {
		b.WriteString("- Description:\n")
		b.WriteString(desc)
		b.WriteString("\n")
	}
	goalText := strings.TrimSpace(task.Goal)
	if goalText == "" {
		goalText = "(no explicit goal)"
	}
	b.WriteString("- Goal:\n")
	b.WriteString(goalText)
	b.WriteString("\n\n")
	b.WriteString(formatPriorOutcomes(prior))
	return b.String()
}

func formatPriorOutcomes(prior []metaTaskState) string {
	var b strings.Builder
	b.WriteString("**Prior Task Outcomes:**\n")
	found := false
	for _, t := range prior {
		status := strings.TrimSpace(t.Status)
		if status == "" || strings.EqualFold(status, "pending") {
			continue
		}
		summary := strings.TrimSpace(t.Summary)
		if summary == "" {
			summary = "No summary recorded."
		}
		summary = strings.ReplaceAll(summary, "\n", " ")
		sessionID := strings.TrimSpace(t.SessionID)
		if sessionID == "" {
			sessionID = "n/a"
		}
		fmt.Fprintf(&b, "- %s: %s (Status: %s, Session: %s)\n", t.Title, summary, status, sessionID)
		found = true
	}
	if !found {
		b.WriteString("- None yet.\n")
	}
	return b.String()
}

func buildMetaEndSummary(task metaTaskState) string {
	var b strings.Builder
	b.WriteString("Task Outcome:\n")
	b.WriteString(fmt.Sprintf("- Title: %s\n", task.Title))
	if task.Step > 0 {
		b.WriteString(fmt.Sprintf("- Step: %d\n", task.Step))
	}
	b.WriteString(fmt.Sprintf("- Shell Agent: %t\n", task.ShellAgent))
	b.WriteString(fmt.Sprintf("- Patch Mode: %t\n", task.PatchMode))
	status := strings.TrimSpace(task.Status)
	if status == "" {
		status = "unknown"
	}
	b.WriteString(fmt.Sprintf("- Status: %s\n", status))
	if desc := strings.TrimSpace(task.Description); desc != "" {
		b.WriteString(fmt.Sprintf("- Description: %s\n", desc))
	}
	if sessionID := strings.TrimSpace(task.SessionID); sessionID != "" {
		b.WriteString(fmt.Sprintf("- Session: %s\n", sessionID))
	}
	if summary := strings.TrimSpace(task.Summary); summary != "" {
		summary = strings.ReplaceAll(summary, "\n", " ")
		b.WriteString(fmt.Sprintf("- Summary: %s\n", summary))
	}
	if transcriptPath := strings.TrimSpace(task.Transcript); transcriptPath != "" {
		b.WriteString(fmt.Sprintf("- Transcript: %s\n", transcriptPath))
	}
	if finalAnswer := strings.TrimSpace(task.FinalAnswer); finalAnswer != "" {
		b.WriteString(fmt.Sprintf("- Final Answer: %s\n", finalAnswer))
	}
	return b.String()
}

func loadPreviousFinalAnswer(prior []metaTaskState) string {
	if len(prior) == 0 {
		return ""
	}
	path := strings.TrimSpace(prior[len(prior)-1].FinalAnswer)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Meta final answer read error (%s): %v\n", path, err)
		return ""
	}
	return strings.TrimSpace(string(data))
}

func composeTaskPrompt(sessionGoal, taskGoal, taskTitle, priorFinalAnswer string, isFirstTask bool) string {
	sessionGoal = strings.TrimSpace(sessionGoal)
	taskGoal = strings.TrimSpace(taskGoal)
	taskTitle = strings.TrimSpace(taskTitle)
	priorFinalAnswer = strings.TrimSpace(priorFinalAnswer)

	taskContent := extractTaskFocus(taskGoal)
	if taskContent == "" {
		taskContent = taskTitle
	}
	if taskContent == "" {
		taskContent = taskGoal
	}
	taskContent = strings.TrimSpace(taskContent)

	sections := make([]string, 0, 2)
	if isFirstTask {
		if taskContent != "" {
			if strings.HasPrefix(taskContent, "***") && strings.HasSuffix(taskContent, "***") {
				sections = append(sections, taskContent)
			} else {
				sections = append(sections, fmt.Sprintf("***%s***", taskContent))
			}
		}
		if sessionGoal != "" {
			sections = append(sections, sessionGoal)
		}
	} else {
		if taskContent != "" {
			sections = append(sections, taskContent)
		}
		if priorFinalAnswer != "" {
			sections = append(sections, priorFinalAnswer)
			if taskContent != "" {
				sections = append(sections, taskContent)
			}
		}
	}

	return strings.Join(sections, "\n\n")
}

func extractTaskFocus(taskGoal string) string {
	if taskGoal == "" {
		return ""
	}
	marker := "\n\nFocus:"
	if idx := strings.Index(taskGoal, marker); idx >= 0 {
		return strings.TrimSpace(taskGoal[idx+len(marker):])
	}
	marker = "Focus:"
	if idx := strings.Index(taskGoal, marker); idx >= 0 {
		return strings.TrimSpace(taskGoal[idx+len(marker):])
	}
	return ""
}

type metaTaskRunResult struct {
	SessionID       string
	Summary         string
	TranscriptPath  string
	FinalAnswerPath string
}

func runMetaTask(ctx metaContext, task metaTaskState, priorFinalAnswer string, index int) (metaTaskRunResult, error) {
	prompt := composeTaskPrompt(ctx.Goal, task.Goal, task.Title, priorFinalAnswer, index == 0)
	if ctx.Config.dryRun {
		sessionID := fmt.Sprintf("dry-run-%d", time.Now().UnixNano())
		return metaTaskRunResult{
			SessionID:       sessionID,
			Summary:         fmt.Sprintf("[dry-run] would execute task %q", task.Title),
			TranscriptPath:  "",
			FinalAnswerPath: "",
		}, nil
	}

	childOptions := ctx.Options
	childOptions.Goal = prompt
	childOptions.Config.SessionID = ""
	childOptions.Config.ParentSessionID = ctx.SessionID
	childOptions.Config.Mode = ""
	childOptions.Config.PromptText = prompt
	childOptions.Config.MetaInstructionDir = ctx.Config.metaInstructionDir
	childOptions.Config.FinalFile = ""
	childOptions.Config.TranscriptFile = ""
	childOptions.Config.FileDiscoveryTrajectory = ""
	childOptions.Config.FileDiscoveryOutputDir = ""
	childOptions.Config.TrajectoryFile = ""
	applyModeDefaults(&childOptions.Config, task.Mode)
	applyTaskOverrides(&childOptions, task)
	if ctx.RootCtx != nil {
		childOptions.Context = ctx.RootCtx
	}
	childOptions.ProcessTimerManager = ctx.Options.ProcessTimerManager

	origTempRoot, tempExists := os.LookupEnv("MACHTIANI_SESSION_TEMP_ROOT")
	if tempExists {
		_ = os.Unsetenv("MACHTIANI_SESSION_TEMP_ROOT")
	}
	defer func() {
		if tempExists {
			_ = os.Setenv("MACHTIANI_SESSION_TEMP_ROOT", origTempRoot)
		} else {
			_ = os.Unsetenv("MACHTIANI_SESSION_TEMP_ROOT")
		}
	}()

	res := Run(ctx.RootCtx, childOptions)
	if res.Err != nil || res.ExitCode != 0 {
		if res.Err != nil {
			return metaTaskRunResult{SessionID: res.SessionID}, fmt.Errorf("meta task %q failed: %w", task.Title, res.Err)
		}
		return metaTaskRunResult{SessionID: res.SessionID}, fmt.Errorf("meta task %q failed with exit code %d", task.Title, res.ExitCode)
	}

	chatDir, err := artifacts.SessionChatDirectory(res.SessionID)
	if err != nil {
		return metaTaskRunResult{SessionID: res.SessionID}, err
	}
	transcriptPath := filepath.Join(chatDir, "agent-transcript.md")
	finalAnswerPath := filepath.Join(chatDir, "agent-final-answer.md")

	summary := loadMetaSummary(finalAnswerPath)
	if summary == "" {
		summary = fmt.Sprintf("Completed task %q", task.Title)
	}

	return metaTaskRunResult{
		SessionID:       res.SessionID,
		Summary:         summary,
		TranscriptPath:  transcriptPath,
		FinalAnswerPath: finalAnswerPath,
	}, nil
}

func loadMetaSummary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return parser.ExtractAnswerSummary(string(data))
}

func renderMetaSummary(goal string, plan metaPlanState) string {
	var b strings.Builder
	b.WriteString("# Meta-Orchestrator Summary\n\n")
	b.WriteString("## Primary Goal\n")
	b.WriteString(goal)
	b.WriteString("\n\n")
	b.WriteString("## Tasks\n")
	for idx, task := range plan.Tasks {
		fmt.Fprintf(&b, "%d. %s (mode: %s)\n", idx+1, task.Title, task.Mode)
		if task.Step > 0 {
			fmt.Fprintf(&b, "   - Step: %d\n", task.Step)
		}
		fmt.Fprintf(&b, "   - Shell Agent: %t\n", task.ShellAgent)
		fmt.Fprintf(&b, "   - Patch Mode: %t\n", task.PatchMode)
		if desc := strings.TrimSpace(task.Description); desc != "" {
			fmt.Fprintf(&b, "   - Description: %s\n", desc)
		}
		if task.SessionID != "" {
			fmt.Fprintf(&b, "   - Session: %s\n", task.SessionID)
		}
		if task.Status != "" {
			fmt.Fprintf(&b, "   - Status: %s\n", task.Status)
		}
		if summary := strings.TrimSpace(task.Summary); summary != "" {
			fmt.Fprintf(&b, "   - Summary: %s\n", summary)
		}
		if task.FinalAnswer != "" {
			fmt.Fprintf(&b, "   - Final Answer: %s\n", task.FinalAnswer)
		}
		if task.Transcript != "" {
			fmt.Fprintf(&b, "   - Transcript: %s\n", task.Transcript)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func tasksToDisplay(tasks []metaTaskState) []ui.MetaTaskDisplay {
	displays := make([]ui.MetaTaskDisplay, 0, len(tasks))
	for idx, task := range tasks {
		displays = append(displays, ui.MetaTaskDisplay{
			Index:     idx + 1,
			Title:     task.Title,
			Mode:      task.Mode,
			Status:    task.Status,
			SessionID: task.SessionID,
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
	opts.Config.ShellAgent = task.ShellAgent
	opts.Config.Patch = task.PatchMode
	if !task.PatchMode {
		opts.Config.PatchStrict = false
	}
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
