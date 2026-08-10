package main

import (
	"testing"

	"github.com/tursomari/machtiani/agent/internal/session"
)

func parseRunDisplayFlags(t *testing.T, args ...string) session.Config {
	t.Helper()
	cfg := session.Config{}
	runFlags := newRunFlagSet(&cfg)
	if err := runFlags.fs.Parse(args); err != nil {
		t.Fatalf("parse run flags %v: %v", args, err)
	}
	return cfg
}

func TestRunFlagDisplayModesDefaultToFalse(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--text", "test prompt")
	if cfg.Focused || cfg.NoShellSteps {
		t.Fatalf("display mode defaults = focused:%t no-shell-steps:%t, want both false", cfg.Focused, cfg.NoShellSteps)
	}
}

func TestRunFlagFocused(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--text", "test prompt", "--focused")
	if !cfg.Focused {
		t.Fatal("--focused did not set session config")
	}
}

func TestRunFlagNoShellSteps(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--text", "test prompt", "--no-shell-steps")
	if !cfg.NoShellSteps {
		t.Fatal("--no-shell-steps did not set session config")
	}
}

func TestRunFlagFocusedAndNoShellStepsCompose(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--text", "test prompt", "--focused", "--no-shell-steps")
	if !cfg.Focused || !cfg.NoShellSteps {
		t.Fatalf("composed display modes = focused:%t no-shell-steps:%t, want both true", cfg.Focused, cfg.NoShellSteps)
	}
}

func TestRunFlagPrint(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--text", "test prompt")
	if cfg.Print {
		t.Fatal("print default = true, want false")
	}

	cfgLong := parseRunDisplayFlags(t, "--text", "test prompt", "--print")
	if !cfgLong.Print {
		t.Fatal("--print did not set session config")
	}

	cfgShort := parseRunDisplayFlags(t, "--text", "test prompt", "-p")
	if !cfgShort.Print {
		t.Fatal("-p shorthand did not set session config")
	}

	runFlags := newRunFlagSet(&session.Config{})
	if runFlags.fs.Lookup("print") == nil {
		t.Fatal("expected --print flag to be registered")
	}
	if runFlags.fs.ShorthandLookup("p") == nil {
		t.Fatal("expected -p shorthand to be registered")
	}
	if runFlags.fs.Lookup("print").DefValue != "false" {
		t.Errorf("--print default = %q, want false", runFlags.fs.Lookup("print").DefValue)
	}
}

func TestRunFlagDisplayModesParseForResume(t *testing.T) {
	for _, tc := range []struct {
		name        string
		flag        string
		wantFocused bool
		wantNoShell bool
	}{
		{name: "focused", flag: "--focused", wantFocused: true},
		{name: "no shell steps", flag: "--no-shell-steps", wantNoShell: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := parseRunDisplayFlags(t, "--session", "agent-resume", tc.flag)
			if cfg.SessionID != "agent-resume" || cfg.Focused != tc.wantFocused || cfg.NoShellSteps != tc.wantNoShell {
				t.Fatalf("resume config = %#v", cfg)
			}
		})
	}
}
