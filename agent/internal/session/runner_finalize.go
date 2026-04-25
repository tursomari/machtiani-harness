package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/conversation"
	mctpatcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
	"github.com/tursomari/machtiani/agent/internal/parser"
	"github.com/tursomari/machtiani/agent/internal/planner"
	"github.com/tursomari/machtiani/agent/internal/runner"
	"github.com/tursomari/machtiani/agent/internal/transcript"
	"github.com/tursomari/machtiani/agent/internal/ui"
)

func (r *runLifecycleState) completeSession(display *ui.TerminalDisplay, answer string, transcriptStep, turnsCompleted int, capped bool) error {
	if err := r.recorder.WriteFinal(answer, transcriptStep, capped); err != nil {
		r.sessionErr = err
		r.turnsCompleted = turnsCompleted
		return err
	}
	finalAnswer := appendFinalAnswerExtras(answer, r.sessionID, r.cfg.verbose)
	if err := writeFinalAnswer(r.sessionID, finalAnswer, r.cfg.finalFile, r.cfg.verbose, r.cfg.dryRun); err != nil {
		r.sessionErr = err
		r.turnsCompleted = turnsCompleted
		return err
	}
	presentFinalAnswer(display, finalAnswer)
	display.EndSession()
	r.turnsCompleted = turnsCompleted
	r.sessionStatus = "success"
	r.keepSessionState = true
	// Mark the meta-plan task as complete (no-op if no meta-plan exists).
	if err := CompleteMetaPlanTask(r.sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to update meta plan task status: %v\n", err)
	}
	state := r.baseSessionState()
	r.pendingState = &state
	r.hydrateState(r.pendingState)
	r.printResumeHint("=== SESSION COMPLETE ===", r.turnsCompleted)
	return nil
}

func (r *runLifecycleState) maybeRunPreFinalizePatch(display *ui.TerminalDisplay, patcherPromptOpts *ui.PromptOptions, pl *planner.Client, conv *conversation.Conversation, pRunner *runner.PatcherRunner, parentSpanID string) *Result {
	if !r.cfg.patch {
		return nil
	}

	var finalizePatchPlan *PatchPlan
	if loadedPlan, err := LoadPatchPlan(r.sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load patch plan: %v\n", err)
	} else {
		finalizePatchPlan = loadedPlan
	}
	if finalizePatchPlan != nil && !finalizePatchPlan.AllComplete() {
		fmt.Fprintf(os.Stderr, "Warning: finalizing with incomplete patch plan\n")
	}

	step := countTurns(r.tr.Content()) + 1
	ctx, cancel := makeTurnContext(r.rootCtx, r.cfg.timeoutPerTurn)
	trFull := r.tr.Content()
	ctx = attachTrajectory(ctx, r.trajectoryWriter, parentSpanID)
	pl.UpdateProgress(r.plannerProgress.snapshot())
	lastDec, lastBody, err := pl.Plan(ctx, conv, r.goal, trFull, step, r.cfg.maxSteps, finalizePatchPlan)
	if cancel != nil {
		cancel()
	}
	if err != nil || lastDec != planner.DecisionPatch {
		return nil
	}
	if pRunner == nil {
		if r.cfg.verbose {
			fmt.Fprintln(os.Stderr, "[patcher] skipping pre-finalize patch: patch runner disabled")
		}
		return nil
	}

	stream := display.BeginPrompt("Patcher: pre-finalize patch", patcherPromptOpts)
	if r.cfg.verbose {
		fmt.Fprintln(os.Stderr, "[patcher] pre-finalize planner payload (raw):", trimTo(strings.TrimSpace(lastBody), 1200))
	}
	jsonBytes, jerr := parser.ExtractPatchJSONPayload(lastBody)
	handled := false
	if jerr != nil {
		stream.Abort("invalid patch payload")
		stream = nil
		_ = r.recorder.WriteTurn(step, "Patcher: pre-finalize (invalid JSON)", "", nil, jerr.Error(), "patch-error")
		handled = true
	}
	if !handled {
		if r.cfg.verbose {
			fmt.Fprintln(os.Stderr, "[patcher] pre-finalize extracted JSON:", trimTo(string(jsonBytes), 1200))
		}
		var instr mctpatcher.Instructions
		dec := json.NewDecoder(bytes.NewReader(jsonBytes))
		dec.DisallowUnknownFields()
		if derr := dec.Decode(&instr); derr != nil {
			stream.Abort("invalid patch payload")
			stream = nil
			_ = r.recorder.WriteTurn(step, "Patcher: pre-finalize (decode error)", "", nil, derr.Error(), "patch-error")
			handled = true
		} else if err := pRunner.Resolve(); err != nil {
			stream.Abort("patcher resolve failed")
			stream = nil
			_ = r.recorder.WriteTurn(step, "Patcher: pre-finalize (resolve failed)", "", nil, err.Error(), "patch-error")
			handled = true
		} else {
			ctxP, cancelP := makeTurnContext(r.rootCtx, r.cfg.timeoutPerTurn)
			ctxP = attachTrajectory(ctxP, r.trajectoryWriter, parentSpanID)
			result, applyErr := pRunner.Apply(ctxP, instr, r.cfg.verbose)
			var ctxPErr error
			if ctxP != nil {
				ctxPErr = ctxP.Err()
			}
			if cancelP != nil {
				cancelP()
			}
			if applyErr != nil {
				if r.isContextCancelled(applyErr) || r.isContextCancelled(ctxPErr) {
					if stream != nil {
						stream.Abort("interrupted")
						stream = nil
					}
					interrupted := r.interruptedResult(applyErr)
					return &interrupted
				}
				var cleanErr *mctpatcher.PatchNotCleanError
				if errors.As(applyErr, &cleanErr) {
					if stream != nil {
						stream.Abort("patch validation failed")
						stream = nil
					}
					rec := transcript.PatchValidationRecord{
						Operation:  cleanErr.Diagnostics.Operation,
						Status:     "failed",
						PatchInput: trimTo(string(jsonBytes), 1000),
						Stderr:     trimTo(cleanErr.Diagnostics.Stderr, 800),
						Error:      strings.TrimSpace(cleanErr.Error()),
						Messages:   convertPatchMessages(cleanErr.Diagnostics.Messages),
						Conflicts:  formatContentConflicts(cleanErr.Diagnostics.ContentConflicts),
					}
					_ = r.recorder.RecordPatchValidation(step, rec)
				} else {
					if stream != nil {
						stream.Abort("patcher execution error")
						stream = nil
					}
					_ = r.recorder.WriteTurn(step, "Patcher: pre-finalize (error)", "", nil, trimTo(applyErr.Error(), 800), "patch-error")
				}
				handled = true
			} else if result == nil {
				if stream != nil {
					stream.Abort("patcher returned no result")
					stream = nil
				}
				_ = r.recorder.WriteTurn(step, "Patcher: pre-finalize (empty result)", "", nil, "patcher returned empty result", "patch-error")
				handled = true
			} else {
				qline := "Patcher: pre-finalize"
				if result.Description != "" {
					qline = "Patcher: pre-finalize - " + strings.TrimSpace(result.Description)
				}
				workspaceStatus := "workspace_applied: no"
				if result.AppliedInWorkspace {
					workspaceStatus = "workspace_applied: yes"
				} else if r.cfg.dryRun {
					workspaceStatus = "workspace_applied: (dry-run)"
				}
				finalizeStatus := "finalize: pending"
				switch {
				case r.cfg.dryRun:
					finalizeStatus = "finalize: (dry-run)"
				case r.cfg.patchNoApply:
					finalizeStatus = "finalize: skipped (--patch-no-apply)"
				}
				diffText, diffErr := patchDiffForTranscript(result.PatchPath, patchTranscriptDiffLimit)
				if diffErr != nil {
					filesSummary := "(none)"
					if len(result.FilesModified) > 0 {
						filesSummary = strings.Join(result.FilesModified, ", ")
					}
					diffText = fmt.Sprintf(
						"Patch diff unavailable (%v)\nPatch path: %s\nSequence: %d\nFiles modified: %s\ninsertions: %d\ndeletions: %d\n%s\n%s",
						diffErr,
						strings.TrimSpace(result.PatchPath),
						result.Sequence,
						filesSummary,
						result.Insertions,
						result.Deletions,
						workspaceStatus,
						finalizeStatus,
					)
				}
				_ = r.recorder.WriteTurn(step, qline, "", nil, diffText, "patch")
				if stream != nil {
					stream.Complete(diffText)
				}
				handled = true
			}
		}
	}
	if !handled && stream != nil {
		stream.Abort("no patch output")
	}
	return nil
}
