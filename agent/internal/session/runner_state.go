package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	"github.com/tursomari/machtiani/agent/internal/llm"
	patchersvc "github.com/tursomari/machtiani/agent/internal/patcher"
	"github.com/tursomari/machtiani/agent/internal/session/fulldiff"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/transcript"
)

type conversationRecorder struct {
	tr                   *transcript.Transcript
	sessionID            string
	conversationGoal     string
	conversationPath     string
	resumeMode           bool
	loadedState          *SessionState
	conversation         *conversation.Conversation
	conversationRendered string
	conversationJSON     string
}

var errConversationTranscriptDesync = errors.New("conversation transcript desync")

func newConversationRecorder(tr *transcript.Transcript, sessionID, conversationGoal, conversationPath string, resumeMode bool, loadedState *SessionState) *conversationRecorder {
	return &conversationRecorder{
		tr:               tr,
		sessionID:        sessionID,
		conversationGoal: conversationGoal,
		conversationPath: conversationPath,
		resumeMode:       resumeMode,
		loadedState:      loadedState,
	}
}

func (c *conversationRecorder) Load() error {
	if c.resumeMode {
		if data, err := os.ReadFile(c.conversationPath); err == nil {
			conv, err := conversation.Unmarshal(data)
			if err != nil {
				return err
			}
			c.conversation = conv
			c.conversationJSON = string(data)
		} else if c.loadedState != nil && strings.TrimSpace(c.loadedState.ConversationJSON) != "" {
			conv, err := conversation.Unmarshal([]byte(c.loadedState.ConversationJSON))
			if err != nil {
				return err
			}
			c.conversation = conv
			c.conversationJSON = c.loadedState.ConversationJSON
		} else {
			return fmt.Errorf("conversation json missing for resume")
		}
	}
	if c.conversation == nil {
		c.conversation = conversation.New(c.sessionID, c.conversationGoal)
	}
	if strings.TrimSpace(c.conversation.OriginalGoal) == "" {
		c.conversation.OriginalGoal = c.conversationGoal
	}
	rendered, err := c.conversation.ToTranscript()
	if err != nil {
		return err
	}
	c.conversationRendered = rendered
	return nil
}

func (c *conversationRecorder) Save() error {
	if c.conversation == nil {
		return nil
	}
	data, err := c.conversation.Marshal()
	if err != nil {
		return err
	}
	c.conversationJSON = string(data)
	if strings.TrimSpace(c.conversationPath) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.conversationPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(c.conversationPath, data, 0o644)
}

func (c *conversationRecorder) EnsureSaved() {
	if strings.TrimSpace(c.conversationJSON) == "" {
		_ = c.Save()
	}
}

func (c *conversationRecorder) Rendered() string {
	return c.conversationRendered
}

func (c *conversationRecorder) JSON() string {
	return c.conversationJSON
}

func (c *conversationRecorder) Path() string {
	return c.conversationPath
}

func (c *conversationRecorder) Conversation() *conversation.Conversation {
	return c.conversation
}

func (c *conversationRecorder) HasConversation() bool {
	return c.conversation != nil
}

func (c *conversationRecorder) renderDelta() (string, string, error) {
	if c.conversation == nil {
		return "", "", nil
	}
	rendered, err := c.conversation.ToTranscript()
	if err != nil {
		return "", "", err
	}
	if c.conversationRendered != "" && !strings.HasPrefix(rendered, c.conversationRendered) {
		return rendered, "", errConversationTranscriptDesync
	}
	delta := rendered[len(c.conversationRendered):]
	return rendered, delta, nil
}

func (c *conversationRecorder) WriteTurn(step int, question, savedPath string, retrieved []string, summary string, decision string) error {
	if c.conversation == nil || c.tr == nil {
		if c.tr == nil {
			return nil
		}
		return c.tr.WriteTurn(step, question, savedPath, retrieved, summary, decision)
	}
	c.conversation.AddMessage("assistant", question, map[string]any{
		"type":     "ask",
		"turn":     step,
		"decision": decision,
	})
	c.conversation.AddMessage("assistant", summary, map[string]any{
		"type":            "answer",
		"turn":            step,
		"retrieved_files": retrieved,
		"chat_path":       savedPath,
	})
	rendered, delta, err := c.renderDelta()
	if errors.Is(err, errConversationTranscriptDesync) {
		if err := c.tr.WriteTurn(step, question, savedPath, retrieved, summary, decision); err != nil {
			return err
		}
		c.conversationRendered = rendered
		return c.Save()
	}
	if err != nil {
		return err
	}
	if delta != "" {
		if err := c.tr.AppendBlock(delta); err != nil {
			return err
		}
	}
	c.conversationRendered = rendered
	return c.Save()
}

func (c *conversationRecorder) AppendRaw(role, content, metaType string) error {
	if c.conversation == nil || c.tr == nil {
		if c.tr == nil {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(metaType)) {
		case "goal_update", "user_feedback":
			return c.tr.AppendRaw(fmt.Sprintf("\n=== GOAL UPDATE\n\n%s\n", content))
		default:
			return c.tr.AppendRaw(content)
		}
	}
	c.conversation.AddMessage(role, content, map[string]any{"type": metaType})
	rendered, delta, err := c.renderDelta()
	if errors.Is(err, errConversationTranscriptDesync) {
		if err := c.appendRawToTranscript(content, metaType); err != nil {
			return err
		}
		c.conversationRendered = rendered
		return c.Save()
	}
	if err != nil {
		return err
	}
	if delta != "" {
		if err := c.tr.AppendBlock(delta); err != nil {
			return err
		}
	}
	c.conversationRendered = rendered
	return c.Save()
}

func (c *conversationRecorder) WriteFinal(answer string, step int, capped bool) error {
	if c.conversation == nil || c.tr == nil {
		if c.tr == nil {
			return nil
		}
		return c.tr.WriteFinal(answer, step, capped)
	}
	c.conversation.AddMessage("assistant", answer, map[string]any{
		"type":   "final",
		"turns":  step,
		"capped": capped,
	})
	rendered, delta, err := c.renderDelta()
	if errors.Is(err, errConversationTranscriptDesync) {
		if err := c.tr.WriteFinal(answer, step, capped); err != nil {
			return err
		}
		c.conversationRendered = rendered
		return c.Save()
	}
	if err != nil {
		return err
	}
	if delta != "" {
		if err := c.tr.AppendBlock(delta); err != nil {
			return err
		}
	}
	c.conversationRendered = rendered
	return c.Save()
}

func (c *conversationRecorder) RecordFullDiff(step int, file, diff, note string) error {
	if c.conversation == nil {
		return nil
	}
	c.conversation.AddMessage("assistant", note, map[string]any{
		"type": "full_diff",
		"turn": step,
		"file": strings.TrimSpace(file),
		"diff": diff,
	})
	return c.Save()
}

func (c *conversationRecorder) appendRawToTranscript(content, metaType string) error {
	if c.tr == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(metaType)) {
	case "goal_update", "user_feedback":
		return c.tr.AppendRaw(fmt.Sprintf("\n=== GOAL UPDATE\n\n%s\n", content))
	default:
		return c.tr.AppendRaw(content)
	}
}

type runLifecycleState struct {
	rootCtx             context.Context
	cfg                 legacyConfig
	sessionID           string
	goal                string
	originalPrompt      string
	taskDescription     string
	plannerOverlay      string
	metaInstructionPath string
	recorder            *conversationRecorder
	plannerProgress     *plannerProgressTracker
	pendingPatchDraft   *patchTranscriptDraft
	notePrompts         *llm.MCTPromptsConfig
	repoRoot            string
	trajectoryWriter    *trajectory.Writer
	tr                  *transcript.Transcript
	sessionStatus       string
	sessionErr          error
	turnsCompleted      int
	userTurnCounter     int
	interrupted         bool
	keepSessionState    bool
	pendingState        *SessionState
}

func (r *runLifecycleState) interruptedResult(err error) Result {
	r.interrupted = true
	if err == nil {
		err = context.Canceled
	}
	r.sessionErr = err
	r.sessionStatus = "interrupted"
	r.turnsCompleted = r.userTurnCounter
	return Result{ExitCode: 130, Status: r.sessionStatus, Turns: r.turnsCompleted, SessionID: r.sessionID, Err: err}
}

func (r *runLifecycleState) isContextCancelled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if rootCtxErr := r.rootCtx.Err(); rootCtxErr != nil && errors.Is(rootCtxErr, context.Canceled) {
		return true
	}
	return false
}

func (r *runLifecycleState) printResumeHint(header string, turns int) {
	fmt.Fprintf(os.Stdout, "%s\nSession ID: %s\nTurns completed: %d\nGoal so far: %q\n\n", header, r.sessionID, turns, r.goal)
	fmt.Fprintf(os.Stdout, "To continue, provide your next instruction, for example:\n  mct-agent run \"<next instruction>\" --session-id %s\n", r.sessionID)
	if parentID := strings.TrimSpace(r.cfg.parentSessionID); parentID != "" {
		fmt.Fprintf(os.Stdout, "\nParent session detected (%s). To resume that session, rerun your original command with the parent session ID, for example:\n  mct-agent run \"<original prompt>\" --session-id %s\n", parentID, parentID)
	}
	fmt.Fprintln(os.Stdout)
}

func (r *runLifecycleState) applyPlannerProgress(state *SessionState) {
	if state == nil {
		return
	}
	state.PlannerProgress = r.plannerProgress.toState()
	if r.pendingPatchDraft != nil {
		state.PendingPatchTurn = &PendingPatchTurnState{
			Step:        r.pendingPatchDraft.Step,
			Description: r.pendingPatchDraft.Description,
			Answer:      r.pendingPatchDraft.Answer,
		}
	} else {
		state.PendingPatchTurn = nil
	}
	if state.PlannerProgress != nil {
		if err := UpdateMetaPlanProgress(r.sessionID, state.PlannerProgress); err != nil && r.cfg.verbose {
			fmt.Fprintf(os.Stderr, "Warning: failed to update meta plan progress for %s: %v\n", r.sessionID, err)
		}
	}
}

func (r *runLifecycleState) metaInstructionDir() string {
	dir := strings.TrimSpace(r.cfg.metaInstructionDir)
	if dir == "" && strings.TrimSpace(r.metaInstructionPath) != "" {
		dir = filepath.Dir(r.metaInstructionPath)
	}
	return dir
}

func (r *runLifecycleState) baseSessionState() SessionState {
	state := SessionState{
		SessionID:       r.sessionID,
		Goal:            r.goal,
		OriginalPrompt:  r.originalPrompt,
		TaskDescription: r.taskDescription,
		PlannerOverlay:  r.plannerOverlay,
		TurnsCompleted:  r.turnsCompleted,
	}
	if r.tr != nil {
		state.TranscriptPath = r.tr.Path()
		state.Transcript = r.tr.Content()
	}
	return state
}

func (r *runLifecycleState) hydrateState(state *SessionState) {
	if state == nil {
		return
	}
	if r.recorder != nil && r.recorder.HasConversation() {
		r.recorder.EnsureSaved()
		if state.ConversationPath == "" {
			state.ConversationPath = r.recorder.Path()
		}
		if state.ConversationJSON == "" {
			state.ConversationJSON = r.recorder.JSON()
		}
	}
	if state.TranscriptPath == "" && r.tr != nil {
		state.TranscriptPath = r.tr.Path()
	}
	if state.Transcript == "" && r.tr != nil {
		state.Transcript = r.tr.Content()
	}
	if strings.TrimSpace(state.MetaInstructionDir) == "" {
		state.MetaInstructionDir = r.metaInstructionDir()
	}
	if len(state.MetaModes) == 0 && strings.TrimSpace(r.cfg.mode) != "" {
		state.MetaModes = []string{strings.ToLower(strings.TrimSpace(r.cfg.mode))}
	}
	state.ParentSessionID = strings.TrimSpace(r.cfg.parentSessionID)
	r.applyPlannerProgress(state)
}

func (r *runLifecycleState) persistSessionState() {
	if r.interrupted {
		state := r.baseSessionState()
		r.hydrateState(&state)
		if err := SaveSessionState(state); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save session state for %s: %v\n", r.sessionID, err)
		} else {
			r.printResumeHint("=== SESSION INTERRUPTED ===", r.turnsCompleted)
		}
		return
	}
	sid := strings.TrimSpace(r.sessionID)
	if sid == "" {
		return
	}
	if r.keepSessionState {
		state := r.baseSessionState()
		if r.pendingState != nil {
			state = *r.pendingState
		}
		r.hydrateState(&state)
		if err := SaveSessionState(state); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to save session state for %s: %v\n", sid, err)
		}
		return
	}
	if err := RemoveSessionState(sid); err != nil && r.cfg.verbose {
		fmt.Fprintf(os.Stderr, "Warning: failed to remove session state for %s: %v\n", sid, err)
	}
}

func (r *runLifecycleState) writePendingPatchTranscript(status string, decision string, note string, undo bool) error {
	if r.pendingPatchDraft == nil {
		return nil
	}
	desc := strings.TrimSpace(r.pendingPatchDraft.Description)
	if desc == "" {
		desc = "Patch applied"
	}
	normalized := strings.ToLower(strings.TrimSpace(status))
	if normalized != "success" && normalized != "reject" {
		normalized = status
	}
	suffix := desc
	if strings.TrimSpace(suffix) != "" {
		suffix = " - " + strings.TrimSpace(suffix)
	}
	question := fmt.Sprintf("Patcher: %s%s", normalized, suffix)
	summary := r.pendingPatchDraft.Answer
	if strings.ToLower(status) == "rejected" || strings.ToLower(status) == "reject" {
		summary = ""
	}
	if strings.ToLower(status) == "success" {
		question = desc
		if noteText := transcript.PatchSuccessNoteText(r.notePrompts); noteText != "" {
			question = desc + "\n\n" + noteText
		}
		decision = ""
	}
	additional := []string{}
	if trimmedNote := strings.TrimSpace(note); trimmedNote != "" {
		additional = append(additional, "Planner review note: "+trimmedNote)
	}
	if undo {
		additional = append(additional, "Planner applied reverse patch to undo the changes.")
	}
	if len(additional) > 0 {
		summary = strings.TrimRight(summary, "\n")
		if summary != "" {
			summary += "\n\n"
		}
		summary += strings.Join(additional, "\n")
	}
	if err := r.recorder.WriteTurn(r.pendingPatchDraft.Step, question, "", nil, summary, decision); err != nil {
		return err
	}
	if strings.ToLower(status) == "success" {
		baseline, err := patchersvc.EnsureBaseline(r.sessionID, r.repoRoot, time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "[full-diff] Failed to ensure baseline: %v\n", err)
		} else {
			files := []string(nil)
			if review := r.plannerProgress.pendingReviewInfo(); review != nil {
				files = append(files, review.Files...)
			}
			if len(files) == 0 {
				files = append(files, r.plannerProgress.successList()...)
			}
			fulldiff.Inject(r.pendingPatchDraft.Step, r.repoRoot, files, r.tr, r.plannerProgress, fulldiff.Options{
				Verbose:        r.cfg.verbose,
				Baseline:       baseline,
				FullDiffNote:   transcript.FullDiffNoteText(r.notePrompts),
				RecordFullDiff: r.recorder.RecordFullDiff,
			})
			if r.trajectoryWriter != nil {
				r.trajectoryWriter.Emit(r.rootCtx, trajectory.Event{
					Kind: "transcript_synthetic_full_diff",
					Payload: map[string]any{
						"op":    "full_diff",
						"step":  r.pendingPatchDraft.Step + 1,
						"files": files,
					},
				})
			}
			r.userTurnCounter += len(files)
		}
	}
	r.pendingPatchDraft = nil
	if r.pendingState != nil {
		r.pendingState.PendingPatchTurn = nil
	}
	return nil
}
