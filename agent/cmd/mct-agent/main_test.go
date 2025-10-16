package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/mct/llm"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
	"github.com/tursomari/machtiani/agent/internal/trajectory/listener"
)

func TestResolveFileDiscoveryTrajectoryConflictingFlags(t *testing.T) {
	cfg := config{
		fileDiscoveryTrajectory: "one",
		fileDiscoveryOutputDir:  "two",
	}
	if _, err := resolveFileDiscoveryTrajectory(cfg, "session"); err == nil {
		t.Fatalf("expected error when both trajectory and output dir flags set")
	}
}

func TestResolveFileDiscoveryTrajectoryOverrideCreatesDir(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nested", "traj.jsonl")
	cfg := config{fileDiscoveryTrajectory: path}
	got, err := resolveFileDiscoveryTrajectory(cfg, "session")
	if err != nil {
		t.Fatalf("resolveFileDiscoveryTrajectory returned error: %v", err)
	}
	if got != path {
		t.Fatalf("expected path %q, got %q", path, got)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
		t.Fatalf("expected directory %q to exist: %v", filepath.Dir(path), err)
	}
}

func TestResolveFileDiscoveryTrajectoryDefaultDir(t *testing.T) {
	cfg := config{dryRun: true}
	sessionID := "agent-20240101"
	got, err := resolveFileDiscoveryTrajectory(cfg, sessionID)
	if err != nil {
		t.Fatalf("resolveFileDiscoveryTrajectory returned error: %v", err)
	}
	expected, err := artifacts.FileDiscoveryTrajectoryPath(sessionID)
	if err != nil {
		t.Fatalf("failed to resolve default file discovery path: %v", err)
	}
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestResolveFileDiscoveryTrajectoryCustomDir(t *testing.T) {
	cfg := config{fileDiscoveryOutputDir: "custom-dir", dryRun: true}
	sessionID := "agent-20240202"
	got, err := resolveFileDiscoveryTrajectory(cfg, sessionID)
	if err != nil {
		t.Fatalf("resolveFileDiscoveryTrajectory returned error: %v", err)
	}
	absDir, err := filepath.Abs(cfg.fileDiscoveryOutputDir)
	if err != nil {
		t.Fatalf("failed to compute absolute dir: %v", err)
	}
	expected := filepath.Join(absDir, "file-discovery.jsonl")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestEnsureFallbackToPrimaryAddsAliasAndResolved(t *testing.T) {
	primary := modelRuntime{
		alias: "good",
		resolved: llm.ResolvedModel{
			Model:    "good-model",
			BaseURL:  "https://example.com",
			Endpoint: "/chat/completions",
		},
	}
	target := modelRuntime{
		alias: "bad",
		resolved: llm.ResolvedModel{
			Model:    "bad-model",
			BaseURL:  "https://bad.example.com",
			Endpoint: "/chat/completions",
		},
	}

	ensureFallbackToPrimary(&target, primary)

	if got, want := len(target.fallbackAliases), 1; got != want {
		t.Fatalf("unexpected fallback alias count: got %d want %d", got, want)
	}
	if target.fallbackAliases[0] != "good" {
		t.Fatalf("unexpected fallback alias: got %q want %q", target.fallbackAliases[0], "good")
	}
	if got, want := len(target.fallbackResolved), 1; got != want {
		t.Fatalf("unexpected fallback resolved count: got %d want %d", got, want)
	}
	if target.fallbackResolved[0].Model != "good-model" {
		t.Fatalf("unexpected fallback resolved model: got %q want %q", target.fallbackResolved[0].Model, "good-model")
	}
}

func TestEnsureFallbackToPrimaryNoDuplicates(t *testing.T) {
	primary := modelRuntime{
		alias: "good",
		resolved: llm.ResolvedModel{
			Model:    "good-model",
			BaseURL:  "https://example.com",
			Endpoint: "/chat/completions",
		},
	}
	target := modelRuntime{
		alias: "good",
		resolved: llm.ResolvedModel{
			Model:    "good-model",
			BaseURL:  "https://example.com",
			Endpoint: "/chat/completions",
		},
		fallbackAliases: []string{"good"},
		fallbackResolved: []llm.ResolvedModel{{
			Model:    "good-model",
			BaseURL:  "https://example.com",
			Endpoint: "/chat/completions",
		}},
	}

	ensureFallbackToPrimary(&target, primary)

	if got, want := len(target.fallbackAliases), 1; got != want {
		t.Fatalf("unexpected fallback alias count: got %d want %d", got, want)
	}
	if got, want := len(target.fallbackResolved), 1; got != want {
		t.Fatalf("unexpected fallback resolved count: got %d want %d", got, want)
	}
}

func TestFailoverLogTrackerSuppressesDuplicates(t *testing.T) {
	tracker := newFailoverLogTracker()
	sessionID := "session-1"
	fromModel := map[string]any{
		"alias":    "primary",
		"model":    "primary-model",
		"base_url": "https://api.example.com",
		"endpoint": "/chat/completions",
	}
	toModel := map[string]any{
		"alias":    "fallback-1",
		"model":    "fallback-model",
		"base_url": "https://api.example.com",
		"endpoint": "/chat/completions",
	}
	startEvent := listener.Event{
		Event: trajectory.Event{
			Kind:         "llm.failover.start",
			ParentSpanID: "turn-1",
			Payload: map[string]any{
				"alias":  "fallback-1",
				"source": "alias",
				"from":   fromModel,
				"to":     toModel,
			},
		},
		SessionID: sessionID,
	}
	if msg := tracker.process(startEvent); !strings.Contains(msg, "triggered") {
		t.Fatalf("expected failover start message, got %q", msg)
	}
	if msg := tracker.process(startEvent); msg != "" {
		t.Fatalf("expected duplicate start to be suppressed, got %q", msg)
	}
	resultEvent := listener.Event{
		Event: trajectory.Event{
			Kind:         "llm.failover.result",
			ParentSpanID: "turn-1",
			Payload: map[string]any{
				"alias":   "fallback-1",
				"source":  "alias",
				"from":    fromModel,
				"to":      toModel,
				"success": true,
			},
		},
		SessionID: sessionID,
	}
	if msg := tracker.process(resultEvent); !strings.Contains(msg, "succeeded") {
		t.Fatalf("expected failover result message, got %q", msg)
	}
	if msg := tracker.process(resultEvent); msg != "" {
		t.Fatalf("expected duplicate result to be suppressed, got %q", msg)
	}
}

func TestFailoverLogTrackerRecoveryResets(t *testing.T) {
	tracker := newFailoverLogTracker()
	sessionID := "session-2"
	fromModel := map[string]any{
		"alias":    "primary",
		"model":    "primary-model",
		"base_url": "https://api.example.com",
		"endpoint": "/chat/completions",
	}
	toModel := map[string]any{
		"alias":    "fallback-1",
		"model":    "fallback-model",
		"base_url": "https://api.example.com",
		"endpoint": "/chat/completions",
	}
	startEvent := listener.Event{
		Event: trajectory.Event{
			Kind:         "llm.failover.start",
			ParentSpanID: "turn-1",
			Payload: map[string]any{
				"alias":  "fallback-1",
				"source": "alias",
				"from":   fromModel,
				"to":     toModel,
			},
		},
		SessionID: sessionID,
	}
	tracker.process(startEvent)
	tracker.process(listener.Event{
		Event: trajectory.Event{
			Kind:         "llm.failover.result",
			ParentSpanID: "turn-1",
			Payload: map[string]any{
				"alias":   "fallback-1",
				"source":  "alias",
				"from":    fromModel,
				"to":      toModel,
				"success": true,
			},
		},
		SessionID: sessionID,
	})
	recoveryEvent := listener.Event{
		Event: trajectory.Event{
			Kind: "llm.request.end",
			Payload: map[string]any{
				"alias": "primary",
				"model": map[string]any{
					"alias":    "primary",
					"model":    "primary-model",
					"base_url": "https://api.example.com",
					"provider": "test-provider",
				},
			},
		},
		SessionID: sessionID,
	}
	if msg := tracker.process(recoveryEvent); !strings.Contains(msg, "recovered") {
		t.Fatalf("expected recovery message, got %q", msg)
	}
	if msg := tracker.process(startEvent); !strings.Contains(msg, "triggered") {
		t.Fatalf("expected failover start after recovery, got %q", msg)
	}
}

func TestFailoverLogTrackerResetsPerTurn(t *testing.T) {
	tracker := newFailoverLogTracker()
	sessionID := "session-turns"
	fromModel := map[string]any{
		"alias":    "primary",
		"model":    "primary-model",
		"base_url": "https://api.example.com",
		"endpoint": "/chat/completions",
	}
	toModel := map[string]any{
		"alias":    "fallback-1",
		"model":    "fallback-model",
		"base_url": "https://api.example.com",
		"endpoint": "/chat/completions",
	}
	turn1Start := listener.Event{
		Event: trajectory.Event{
			Kind:         "llm.failover.start",
			ParentSpanID: "turn-1",
			Payload: map[string]any{
				"alias":  "fallback-1",
				"source": "alias",
				"from":   fromModel,
				"to":     toModel,
			},
		},
		SessionID: sessionID,
	}
	if msg := tracker.process(turn1Start); !strings.Contains(msg, "triggered") {
		t.Fatalf("expected failover start message for turn 1, got %q", msg)
	}
	if msg := tracker.process(turn1Start); msg != "" {
		t.Fatalf("expected duplicate turn 1 start to be suppressed, got %q", msg)
	}
	turn1Result := listener.Event{
		Event: trajectory.Event{
			Kind:         "llm.failover.result",
			ParentSpanID: "turn-1",
			Payload: map[string]any{
				"alias":   "fallback-1",
				"source":  "alias",
				"from":    fromModel,
				"to":      toModel,
				"success": true,
			},
		},
		SessionID: sessionID,
	}
	if msg := tracker.process(turn1Result); !strings.Contains(msg, "succeeded") {
		t.Fatalf("expected failover result message for turn 1, got %q", msg)
	}
	if msg := tracker.process(turn1Result); msg != "" {
		t.Fatalf("expected duplicate turn 1 result to be suppressed, got %q", msg)
	}
	turn2Start := turn1Start
	turn2Start.ParentSpanID = "turn-2"
	if msg := tracker.process(turn2Start); !strings.Contains(msg, "triggered") {
		t.Fatalf("expected failover start message for turn 2, got %q", msg)
	}
	turn2Result := turn1Result
	turn2Result.ParentSpanID = "turn-2"
	if msg := tracker.process(turn2Result); !strings.Contains(msg, "succeeded") {
		t.Fatalf("expected failover result message for turn 2, got %q", msg)
	}
}
