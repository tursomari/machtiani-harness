package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

func TestResolveFileDiscoveryTrajectoryConflictingFlags(t *testing.T) {
	cfg := legacyConfig{
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
	cfg := legacyConfig{fileDiscoveryTrajectory: path}
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
	cfg := legacyConfig{dryRun: true}
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
	cfg := legacyConfig{fileDiscoveryOutputDir: "custom-dir", dryRun: true}
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

func TestResolveModelRuntimesAnswerAlias(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	writeTestConfig(t, configPath)
	t.Setenv("MACHTIANI_CONFIG", configPath)

	cfg := legacyConfig{
		orchModel:   "orch",
		answerModel: "answer",
	}
	models, err := resolveModelRuntimes(cfg, nil, nil)
	if err != nil {
		t.Fatalf("resolveModelRuntimes returned error: %v", err)
	}
	if models.answer.alias != "answer" {
		t.Fatalf("expected answer alias 'answer', got %q", models.answer.alias)
	}
	if got := strings.TrimSpace(models.answer.resolved.Model); got != "answer-model" {
		t.Fatalf("expected answer model 'answer-model', got %q", got)
	}
	if len(models.answer.fallbackAliases) == 0 || models.answer.fallbackAliases[0] != "orch" {
		t.Fatalf("expected orchestrator fallback alias, got %v", models.answer.fallbackAliases)
	}
	if len(models.answer.fallbackResolved) == 0 || models.answer.fallbackResolved[0].Model != "orch-model" {
		t.Fatalf("expected orchestrator fallback model, got %+v", models.answer.fallbackResolved)
	}
}

func TestResolveModelRuntimesAnswerDefaultsToOrchestrator(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.toml")
	writeTestConfig(t, configPath)
	t.Setenv("MACHTIANI_CONFIG", configPath)

	cfg := legacyConfig{orchModel: "orch"}
	models, err := resolveModelRuntimes(cfg, nil, nil)
	if err != nil {
		t.Fatalf("resolveModelRuntimes returned error: %v", err)
	}
	if models.answer.resolved.Model != models.orchestrator.resolved.Model {
		t.Fatalf("expected answer model to match orchestrator, got %q vs %q", models.answer.resolved.Model, models.orchestrator.resolved.Model)
	}
	if models.answer.alias != models.orchestrator.alias {
		t.Fatalf("expected answer alias to match orchestrator, got %q vs %q", models.answer.alias, models.orchestrator.alias)
	}
}

func writeTestConfig(t *testing.T, path string) {
	t.Helper()
	content := `default_model = "orch"

[providers.fake]
base_url = "https://example.com"
api_key = "test-key"

[models.orch]
provider = "fake"
model = "orch-model"

[models.answer]
provider = "fake"
model = "answer-model"
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
