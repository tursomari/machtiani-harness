package session

import (
	"fmt"
	"io"

	"github.com/tursomari/machtiani/agent/internal/ui"
)

func (r *runLifecycleState) completeSession(display ui.SessionDisplay, diagWriter io.Writer, answer string, transcriptStep, turnsCompleted int, capped bool) error {
	if err := r.recorder.WriteFinal(answer, transcriptStep, capped); err != nil {
		r.sessionErr = err
		r.turnsCompleted = turnsCompleted
		return err
	}
	finalAnswer := answer
	if err := writeFinalAnswer(r.sessionID, finalAnswer, r.cfg.finalFile, r.cfg.verbose, r.cfg.dryRun, diagWriter); err != nil {
		r.sessionErr = err
		r.turnsCompleted = turnsCompleted
		return err
	}
	presentFinalAnswer(display, finalAnswer, diagWriter)
	display.EndSession()
	r.turnsCompleted = turnsCompleted
	if err := r.transition(StateSuccess); err != nil {
		return err
	}
	// Mark the mode-plan task as complete (no-op if no mode-plan exists).
	if err := CompleteModePlanTask(r.sessionID); err != nil {
		fmt.Fprintf(diagWriter, "Warning: failed to update mode plan task status: %v\n", err)
	}
	state := r.baseSessionState()
	r.pendingState = &state
	r.hydrateState(r.pendingState, diagWriter)
	r.printResumeHint(display, "=== SESSION COMPLETE ===", r.turnsCompleted)
	return nil
}

