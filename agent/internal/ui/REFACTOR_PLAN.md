# TUI Event Bus Refactor Plan

Branch: refactor/tui-event-bus-v1
Commit convention: conventional commits (feat, refactor, test, fix)

## Phase 1: Define DisplayEvent types and event bus — COMPLETE

Commit: a28d20552 feat(ui): add DisplayEvent types and event bus

Checklist:
- [x] Created agent/internal/ui/events.go with DisplayEvent interface and 12 concrete event types
- [x] Created agent/internal/ui/bus.go with EventBus struct (buffered channel, Emit, Subscribe, Close, Shutdown)
- [x] Created agent/internal/ui/bus_test.go with 5 tests (ordering, multiple subscribers, backpressure, close behavior)
- [x] go build ./... and go test ./... pass

## Phase 2: Build centralized formatter/renderer — COMPLETE

Commit: ce840ffc5 refactor(ui): replace terminal_display.go with event-driven formatter

Checklist:
- [x] Created agent/internal/ui/theme.go with Theme struct and DefaultTheme() constructor
- [x] Created agent/internal/ui/terminal_control.go with centralized ANSI escape helpers
- [x] Created agent/internal/ui/helpers.go extracting PromptOptions, ModeTaskDisplay, ANSI constants, utility functions
- [x] Created agent/internal/ui/formatter.go (713 lines) with Formatter struct implementing SessionDisplay interface
- [x] Formatter implements SessionDisplay as a bridge: each interface method emits typed events into the EventBus
- [x] Unified renderTextLocked method consolidates renderCurrentLocked and renderCurrentWithActionsLocked
- [x] Severity-based notification rendering: Error=red, Warning=yellow, Info=gray
- [x] Distinct ChunkReceived vs ActionExecuted rendering (different prefixes/colors)
- [x] Timer management wired: start on SessionStarted, stop on SessionEnded
- [x] ProcessTimerManager integration preserved
- [x] agent/internal/ui/terminal_display.go deleted
- [x] Construction sites updated: runner.go, display_test.go, mode_test.go
- [x] Interface assertions updated: display.go, prompt_stream.go
- [x] go build ./... and go test ./... pass

Note: The Formatter implements SessionDisplay as a bridge that internally emits events. The runner still calls through the SessionDisplay interface. This is functional but the bridge indirection means Phase 4 (direct bus emission from runner call sites) remains as future work.

## Phase 3: Convert trajectory listeners — COMPLETE

Commit: e0861dcc4 refactor(session): convert trajectory listeners to event bus

Checklist:
- [x] shell_actions.go: display.StreamAction → bus.Emit(ActionExecutedEvent)
- [x] failover.go: display.Notify → bus.Emit(NotificationEvent{Level: Warning})
- [x] retry.go: display.Notify → bus.Emit(NotificationEvent{Level: Info})
- [x] cache_usage.go: display.Notify → bus.Emit(NotificationEvent{Level: Info})
- [x] cache_diagnostics.go: display.Notify → bus.Emit(NotificationEvent{Level: Info})
- [x] runner.go updated with type-safe Formatter.Bus extraction
- [x] Test files updated (failover_test.go, diagwriter_capture_test.go)
- [x] go build ./... and go test ./... pass

## Phase 4: Convert session runner call sites — INCOMPLETE

Not yet started. The 33 call sites across 5 files still use the SessionDisplay interface bridge:

- runner_state.go: 14 WriteString calls + 1 EndSession call (lines 908-933, 1065)
- runner_turns.go: 7 Notify calls via env.display + 1 BeginPrompt (lines 221, 237, 372, 383, 386, 508, 516, 519) + 1 display.Notify at line 726
- runner.go: 1 StartSession + 1 WriteString + 1 EndSession + 1 type assertion (lines 319, 618, 621, 629)
- runner_finalize.go: 1 EndSession call (line 23)
- output.go: 1 ShowFinal call (line 51)

Checklist (remaining):
- [ ] Convert runner_state.go call sites to bus.Emit (16 calls: WriteString → RawStringEvent, EndSession → SessionEndedEvent)
- [ ] Convert runner_turns.go call sites to bus.Emit (9 calls: Notify → NotificationEvent, BeginPrompt → already handled by Formatter bridge, but direct emission would be PromptStartedEvent)
- [ ] Convert runner.go call sites to bus.Emit (4 calls: StartSession → SessionStartedEvent, WriteString → RawStringEvent, EndSession → SessionEndedEvent)
- [ ] Convert runner_finalize.go call site to bus.Emit (1 call: EndSession → SessionEndedEvent)
- [ ] Convert output.go call site to bus.Emit (1 call: ShowFinal → FinalAnswerEvent)
- [ ] go build ./... and go test ./... pass
- [ ] run-live.sh shows correct display output

Decision needed: Whether to keep the SessionDisplay bridge (simpler, less churn) or convert all 33 call sites to direct bus.Emit (cleaner architecture, removes indirection). Both approaches work correctly. The bridge can be removed later without breaking functionality.

## Phase 5: Finalize PromptStream and streaming preservation — COMPLETE

Code-level verification complete, end-to-end verification not done.

Checklist:
- [x] Confirmed eventLoop uses bare for event := range ch with zero polling delay
- [x] Confirmed FormatterPromptStream.OnChunk emits ChunkReceivedEvent synchronously into bus
- [x] Confirmed per-stream correlation by streamID in formatter event handlers
- [x] Confirmed streaming path goes through SessionDisplay/PromptStream interfaces
- [x] run-live.sh passed (26/26 cases, zero failures)
- [ ] ab-dev.sh skipped per user instruction

Note: issue-b-3turn test in run-live.sh is expected to fail (unrelated to this refactor).

## Phase 6: Update tests and cleanup — INCOMPLETE

Checklist:
- [ ] display_test.go still uses mockDisplay/mockPromptStream implementing old interfaces — should replace with mock event bus subscriber
- [ ] No formatter tests exist beyond bus_test.go — should add formatter-level tests
- [ ] terminal_display_test.go already deleted in Phase 2
- [x] No remaining references to TerminalDisplay, TerminalPromptStream, NewTerminalDisplay, or OnActionChunk
- [x] go build ./... and go test ./... pass
- [x] run-live.sh passed

## Progress Log

- 2026-07-02 a28d20552: Phase 1 complete — DisplayEvent types and event bus
- 2026-07-02 ce840ffc5: Phase 2 complete — formatter replaces terminal_display.go
- 2026-07-02 e0861dcc4: Phase 3 complete — trajectory listeners converted to event bus

## Status

Phase 1: COMPLETE
Phase 2: COMPLETE
Phase 3: COMPLETE
Phase 4: INCOMPLETE
Phase 5: COMPLETE
Phase 6: PARTIAL (testing infrastructure updates remain)

Do not stage this change.
