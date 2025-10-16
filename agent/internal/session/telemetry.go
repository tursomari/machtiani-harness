package session

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

type sessionTelemetry struct {
	writer    *trajectory.Writer
	sessionID string
	span      trajectory.Span
	started   time.Time
	repoRoot  string
	goal      string
	cfg       legacyConfig
	build     BuildInfo
}

type turnTelemetry struct {
	span     trajectory.Span
	step     int
	maxSteps int
	started  time.Time
}

func newSessionTelemetry(writer *trajectory.Writer, sessionID, goal string, cfg legacyConfig, repoRoot string, build BuildInfo) *sessionTelemetry {
	if writer == nil {
		return nil
	}
	st := &sessionTelemetry{
		writer:    writer,
		sessionID: sessionID,
		span:      writer.StartSpan(""),
		started:   time.Now(),
		repoRoot:  repoRoot,
		goal:      goal,
		cfg:       cfg,
		build:     build,
	}
	payload := map[string]any{
		"event_version": 1,
		"config_summary": map[string]any{
			"max_steps":        cfg.maxSteps,
			"timeout_per_turn": cfg.timeoutPerTurn,
			"dry_run":          cfg.dryRun,
			"patch_enabled":    cfg.patch,
			"patch_no_apply":   cfg.patchNoApply,
		},
		"versions": map[string]any{
			"agent":    build.Version,
			"commit":   build.Commit,
			"built_at": build.BuiltAt,
			"dirty":    build.Dirty,
		},
	}
	if repoRoot != "" && !cfg.trajectoryOmitRepoRoot {
		payload["repo_root"] = repoRoot
	}
	payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(goal, writer.ExcerptLen()), "goal")
	evt := trajectory.Event{Kind: "agent.session.start", SpanID: st.span.ID, Payload: payload}
	if err := writer.Emit(context.Background(), evt); err != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] session start emit error: %v\n", err)
	}
	return st
}

func (st *sessionTelemetry) StartTurn(step, maxSteps int) *turnTelemetry {
	if st == nil || st.writer == nil {
		return nil
	}
	tt := &turnTelemetry{
		span:     st.writer.StartSpan(st.span.ID),
		step:     step,
		maxSteps: maxSteps,
		started:  time.Now(),
	}
	payload := map[string]any{
		"event_version": 1,
		"step":          step,
		"max_steps":     maxSteps,
	}
	if step == 1 {
		payload = trajectory.MergeExcerptWithPrefix(payload, trajectory.MakeTextExcerpt(st.goal, st.writer.ExcerptLen()), "goal")
	}
	evt := trajectory.Event{Kind: "agent.turn.start", SpanID: tt.span.ID, ParentSpanID: st.span.ID, Payload: payload}
	if err := st.writer.Emit(context.Background(), evt); err != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] turn start emit error: %v\n", err)
	}
	return tt
}

func (st *sessionTelemetry) EndTurn(tt *turnTelemetry, decision string, status string, info map[string]any, err error) {
	if st == nil || st.writer == nil || tt == nil {
		return
	}
	payload := map[string]any{
		"event_version": 1,
		"step":          tt.step,
		"max_steps":     tt.maxSteps,
		"decision":      decision,
		"status":        status,
		"duration_ms":   time.Since(tt.started).Milliseconds(),
	}
	for k, v := range info {
		payload[k] = v
	}
	event := trajectory.Event{
		Kind:         "agent.turn.end",
		SpanID:       tt.span.ID,
		ParentSpanID: st.span.ID,
		Payload:      payload,
	}
	if err != nil {
		event.Level = "error"
		event.Err = &trajectory.ErrorInfo{Message: err.Error(), Category: "unknown"}
	}
	if emitErr := st.writer.Emit(context.Background(), event); emitErr != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] turn end emit error: %v\n", emitErr)
	}
}

func (st *sessionTelemetry) Finish(status string, turns int, err error) {
	if st == nil || st.writer == nil {
		return
	}
	payload := map[string]any{
		"event_version": 1,
		"status":        status,
		"turns":         turns,
		"duration_ms":   time.Since(st.started).Milliseconds(),
	}
	event := trajectory.Event{
		Kind:    "agent.session.end",
		SpanID:  st.span.ID,
		Payload: payload,
	}
	if err != nil {
		event.Level = "error"
		event.Err = &trajectory.ErrorInfo{Message: err.Error(), Category: "unknown"}
	}
	if emitErr := st.writer.Emit(context.Background(), event); emitErr != nil {
		fmt.Fprintf(os.Stderr, "[trajectory] session end emit error: %v\n", emitErr)
	}
	_ = st.writer.Close()
}
