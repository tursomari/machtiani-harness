# Plan: Streamline Planner Decision Types

## Goal
Replace the current two-value decision system (Ask, Finalize) with three first-class decisions: AskWorker for delegating work to a sub-agent (same as current Ask), AskUser for directing a question to the user and suspending, AnswerUser for final response to the user (replaces Finalize). Clean up stale patch references.

## Steps

### Step 1: Rename decision constants in planner.go
- [x] Rename DecisionAsk to DecisionAskWorker with value "ask_worker"
- [x] Rename DecisionFinalize to DecisionAnswerUser with value "answer_user"
- [x] Add DecisionAskUser constant with value "ask_user"
- [x] Update all internal references to old names throughout the codebase

### Step 2: Update the plan prompt template
- [x] Edit plan_prompt.tpl: Replace Ask with AskWorker, Finalize with AnswerUser, add AskUser as a third option
- [x] Update examples to reflect new names
- [x] Update the regex line from Decision: ask|finalize to Decision: ask_worker|ask_user|answer_user

### Step 3: Update the format error template
- [x] Remove stale patch reference from format_error_template.tpl

### Step 4: Update buildAskGuardrail fallback message
- [x] Remove mention of Patch must be chosen separately

### Step 5: Rename turn loop actions in runner_turns.go
- [x] Rename turnLoopContinue to turnLoopAskWorker
- [x] Rename turnLoopFinalize to turnLoopAnswerUser
- [x] Add turnLoopAskUser constant with value "ask_user"
- [x] In the orchestrator loop runner.go, handle DecisionAskUser mapping to turnLoopAskUser path that surfaces the question and suspends

### Step 6: Integrate existing AskUser-directed guard
- [ ] Ensure that when planner returns DecisionAskUser, the existing AnalyzeUserDirectedAsk logic is invoked or its result is surfaced
- [ ] The AskUser decision should bypass normal ask generation and instead use the purified question from the guard to present to the user

### Step 7: Build and verify
- [ ] Run go build ./... from agent/ after each logical step
- [ ] Run go test ./... in agent/internal/planner/ to ensure no regressions

### Step 8: A/B test with container
- [ ] Write a minimal integration test that exercises the new decisions in a container
