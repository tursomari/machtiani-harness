package session

import (
	"fmt"
	"os"

	"github.com/tursomari/machtiani/agent/internal/ui"
)

func (r *runLifecycleState) completeSession(display *ui.TerminalDisplay, answer string, transcriptStep, turnsCompleted int, capped bool) error {
	if err := r.recorder.WriteFinal(answer, transcriptStep, capped); err != nil {
		r.sessionErr = err
		r.turnsCompleted = turnsCompleted
		return err
	}
	finalAnswer := answer
	if err := writeFinalAnswer(r.sessionID, finalAnswer, r.cfg.finalFile, r.cfg.verbose, r.cfg.dryRun); err != nil {
		r.sessionErr = err
		r.turnsCompleted = turnsCompleted
		return err
	}
	presentFinalAnswer(display, finalAnswer)
	display.EndSession()
	r.turnsCompleted = turnsCompleted
	if err := r.transition(StateSuccess); err != nil {
		return err
	}
	// Mark the mode-plan task as complete (no-op if no mode-plan exists).
	if err := CompleteModePlanTask(r.sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update mode plan task status: %v\n", err)
	}
	state := r.baseSessionState()
	r.pendingState = &state
	r.hydrateState(r.pendingState)
	r.printResumeHint("=== SESSION COMPLETE ===", r.turnsCompleted)
	return nil
}

