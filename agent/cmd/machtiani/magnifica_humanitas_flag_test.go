package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
	"github.com/tursomari/machtiani/agent/internal/session"
)

func TestRunFlagMagnificaHumanitas(t *testing.T) {
	defaultConfig := session.Config{}
	defaultFlags := newRunFlagSet(&defaultConfig)
	if err := defaultFlags.fs.Parse([]string{"--prompt", "test prompt"}); err != nil {
		t.Fatalf("parse default run flags: %v", err)
	}
	if defaultConfig.MagnificaHumanitas {
		t.Fatal("MagnificaHumanitas default = true, want false")
	}

	enabledConfig := session.Config{}
	enabledFlags := newRunFlagSet(&enabledConfig)
	if err := enabledFlags.fs.Parse([]string{"--prompt", "test prompt", "--magnifica-humanitas"}); err != nil {
		t.Fatalf("parse --magnifica-humanitas: %v", err)
	}
	if !enabledConfig.MagnificaHumanitas {
		t.Fatal("--magnifica-humanitas did not set session config")
	}

	flag := enabledFlags.fs.Lookup("magnifica-humanitas")
	if flag == nil {
		t.Fatal("expected --magnifica-humanitas to be registered")
	}
	if flag.DefValue != "false" || flag.Shorthand != "" || flag.Deprecated != "" {
		t.Fatalf("--magnifica-humanitas surface = default:%q shorthand:%q deprecated:%q, want false with no shorthand or deprecation", flag.DefValue, flag.Shorthand, flag.Deprecated)
	}
	var related []string
	for _, candidate := range allFlagNames(enabledFlags) {
		if strings.Contains(candidate, "magnifica") || strings.Contains(candidate, "humanitas") {
			related = append(related, candidate)
		}
	}
	if len(related) != 1 || related[0] != "magnifica-humanitas" {
		t.Fatalf("Magnifica Humanitas flag names = %v, want only magnifica-humanitas", related)
	}

	combinedConfig := session.Config{}
	combinedFlags := newRunFlagSet(&combinedConfig)
	if err := combinedFlags.fs.Parse([]string{"--magnifica-humanitas", "--no-banner"}); err != nil {
		t.Fatalf("parse --magnifica-humanitas with --no-banner: %v", err)
	}
	if !combinedConfig.MagnificaHumanitas || !combinedConfig.NoBanner {
		t.Fatalf("combined config = MagnificaHumanitas:%t NoBanner:%t, want both true", combinedConfig.MagnificaHumanitas, combinedConfig.NoBanner)
	}
}

func allFlagNames(flags runFlagSetResult) []string {
	var names []string
	flags.fs.VisitAll(func(flag *pflag.Flag) {
		names = append(names, flag.Name)
	})
	return names
}

func TestRunMagnificaHumanitasPersistsInSessionShowJSON(t *testing.T) {
	for _, tc := range []struct {
		name        string
		enabled     bool
		sessionID   string
		wantPresent bool
	}{
		{name: "enabled", enabled: true, sessionID: "agent-managed-magnifica-enabled", wantPresent: true},
		{name: "disabled", enabled: false, sessionID: "agent-managed-magnifica-disabled", wantPresent: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupMagnificaRunCommandTest(t, tc.sessionID)

			args := []string{
				"--dry-run",
				"--max-turns", "1",
				"--no-cursor",
				"--no-trajectory",
				"--prompt", "Record a hermetic session.",
			}
			if tc.enabled {
				args = append(args, "--magnifica-humanitas")
			}

			stdout, stderr := captureOutput(func() {
				if code := handleRunCommand(args); code != 0 {
					t.Fatalf("run exit = %d, want 0", code)
				}
			})
			if strings.Contains(stdout, "machtiani (mct)") {
				t.Fatalf("non-TTY run unexpectedly rendered a banner: %q", stdout)
			}
			if strings.Contains(stderr, "Error") {
				t.Fatalf("run stderr contains an error: %q", stderr)
			}

			showJSON, showStderr := captureOutput(func() {
				if code := handleSessionShowCommand([]string{"--json", tc.sessionID}); code != 0 {
					t.Fatalf("session show --json exit = %d, want 0", code)
				}
			})
			if showStderr != "" {
				t.Fatalf("session show --json stderr = %q", showStderr)
			}
			var state map[string]json.RawMessage
			if err := json.Unmarshal([]byte(showJSON), &state); err != nil {
				t.Fatalf("decode session show JSON: %v\n%s", err, showJSON)
			}
			raw, present := state["magnifica_humanitas"]
			if present != tc.wantPresent {
				t.Fatalf("magnifica_humanitas presence = %t, want %t\n%s", present, tc.wantPresent, showJSON)
			}
			if !present {
				return
			}
			var quote struct {
				Paragraph int    `json:"paragraph"`
				Line      int    `json:"line"`
				Quote     string `json:"quote"`
			}
			if err := json.Unmarshal(raw, &quote); err != nil {
				t.Fatalf("decode magnifica_humanitas: %v", err)
			}
			if quote.Paragraph <= 0 || quote.Line <= 0 || strings.TrimSpace(quote.Quote) == "" {
				t.Fatalf("magnifica_humanitas = %#v, want populated structured quote", quote)
			}
		})
	}
}

func setupMagnificaRunCommandTest(t *testing.T, sessionID string) {
	t.Helper()
	originalHead := readmeHeadCommitFn
	originalCommit := readmeCommitForProjectFn
	originalRun := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = originalHead
		readmeCommitForProjectFn = originalCommit
		sessionRunFn = originalRun
		llm.ResetConfigForTesting()
	})

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MACHTIANI_SESSION_ID", sessionID)
	t.Setenv("MACHTIANI_SESSION_TEMP_ROOT", "")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.invalid/v1")
	t.Setenv("OPENAI_MODEL", "test-model")

	repoDir := initTestRepo(t)
	if err := projectstore.WriteProjectUUID(repoDir, uuid.New()); err != nil {
		t.Fatalf("write project UUID: %v", err)
	}
	originalWD := mustChdir(t, repoDir)
	t.Cleanup(func() { mustChdir(t, originalWD) })

	configPath := filepath.Join(home, ".machtiani", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)
	llm.ResetConfigForTesting()

	readmeHeadCommitFn = func() (string, error) { return "abcdef123456", nil }
	readmeCommitForProjectFn = func(string) (string, error) { return "deadbeef", nil }
	sessionRunFn = func(ctx context.Context, opts session.Options) session.Result {
		return session.Run(ctx, opts)
	}
}
