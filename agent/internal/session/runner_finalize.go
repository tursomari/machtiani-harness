package session

import (
	"fmt"
	"io"

	"github.com/tursomari/machtiani/agent/internal/ui"
)

func (r *runLifecycleState) completeSession(bus *ui.EventBus, diagWriter io.Writer, answer string, transcriptStep, turnsCompleted int, capped bool) error {
	if err := r.recorder.WriteFinal(answer, transcriptStep, capped); err != nil {
		r.sessionErr = err
		r.turnsCompleted = turnsCompleted
		return err
	}
	finalAnswer := answer
	finalAnswerPath, err := writeFinalAnswer(r.sessionID, finalAnswer, r.cfg.finalFile, r.cfg.dryRun)
	if err != nil {
		r.sessionErr = err
		r.turnsCompleted = turnsCompleted
		return err
	}
	renderedAnswer := renderFinalAnswer(finalAnswer, diagWriter, r.presentation)
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
	r.printSessionConclusion(bus, diagWriter, ui.SessionConclusionEvent{
		Outcome:         ui.SessionConclusionCompleted,
		RenderedAnswer:  renderedAnswer,
		FinalAnswerPath: finalAnswerPath,
		SessionID:       r.sessionID,
		Verbose:         r.cfg.verbose,
		TurnsCompleted:  r.turnsCompleted,
		Goal:            r.goal,
	})
	if bus != nil {
		bus.Emit(ui.SessionEndedEvent{})
	}
	return nil
}
