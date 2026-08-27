package main

import (
	"io"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/presentation"
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
	cfg := parseRunDisplayFlags(t, "--prompt", "test prompt")
	if cfg.Focused || cfg.NoShellSteps {
		t.Fatalf("display mode defaults = focused:%t no-shell-steps:%t, want both false", cfg.Focused, cfg.NoShellSteps)
	}
}

func TestRunFlagFocused(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--prompt", "test prompt", "--focused")
	if !cfg.Focused {
		t.Fatal("--focused did not set session config")
	}
}

func TestRunFlagNoShellSteps(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--prompt", "test prompt", "--no-shell-steps")
	if !cfg.NoShellSteps {
		t.Fatal("--no-shell-steps did not set session config")
	}
}

func TestRunFlagFocusedAndNoShellStepsCompose(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--prompt", "test prompt", "--focused", "--no-shell-steps")
	if !cfg.Focused || !cfg.NoShellSteps {
		t.Fatalf("composed display modes = focused:%t no-shell-steps:%t, want both true", cfg.Focused, cfg.NoShellSteps)
	}
}

func TestRunFlagNoCursorParsesForAttach(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--attach", "--resume", "agent-attach", "--no-cursor")
	if cfg.SessionID != "agent-attach" || !cfg.Attach || !cfg.NoCursor {
		t.Fatalf("attach display config = %#v", cfg)
	}
}

func TestResolveAttachThemeUsesPresentationOverridesAndNoCursor(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("MACHTIANI_THEME", "machtiani-light")
	theme, err := resolveAttachTheme(io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	if theme.Profile != presentation.ProfileMachtianiLight || theme.MotionMode() != presentation.MotionFull {
		t.Fatalf("attach theme = profile:%q motion:%q", theme.Profile, theme.MotionMode())
	}
	noCursorTheme, err := resolveAttachTheme(io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	if noCursorTheme.Profile != presentation.ProfileMachtianiLight || noCursorTheme.MotionMode() != presentation.MotionNone {
		t.Fatalf("attach no-cursor theme = profile:%q motion:%q", noCursorTheme.Profile, noCursorTheme.MotionMode())
	}
}

func TestRunFlagDisplayModesComposeForResume(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--resume", "agent-resume", "--focused", "--no-shell-steps")
	if cfg.SessionID != "agent-resume" || !cfg.Focused || !cfg.NoShellSteps {
		t.Fatalf("resume display config = %#v", cfg)
	}
}

func TestRunFlagExec(t *testing.T) {
	cfg := parseRunDisplayFlags(t, "--prompt", "test prompt")
	if cfg.Print {
		t.Fatal("exec default = true, want false")
	}
	if cfg.PromptText != "test prompt" {
		t.Fatalf("prompt text = %q, want %q", cfg.PromptText, "test prompt")
	}

	cfgLong := parseRunDisplayFlags(t, "--prompt", "test prompt", "--exec")
	if !cfgLong.Print {
		t.Fatal("--exec did not set session config")
	}

	cfgShort := parseRunDisplayFlags(t, "--prompt", "test prompt", "-x")
	if !cfgShort.Print {
		t.Fatal("-x shorthand did not set session config")
	}

	runFlags := newRunFlagSet(&session.Config{})
	if runFlags.fs.Lookup("exec") == nil {
		t.Fatal("expected --exec flag to be registered")
	}
	if runFlags.fs.ShorthandLookup("x") == nil {
		t.Fatal("expected -x shorthand to be registered")
	}
	if runFlags.fs.Lookup("exec").DefValue != "false" {
		t.Errorf("--exec default = %q, want false", runFlags.fs.Lookup("exec").DefValue)
	}
	if runFlags.fs.Lookup("prompt") == nil || runFlags.fs.ShorthandLookup("p") == nil {
		t.Fatal("expected --prompt/-p to be registered")
	}
	for _, legacy := range []string{"text", "print"} {
		if runFlags.fs.Lookup(legacy) != nil {
			t.Fatalf("legacy flag --%s must not be registered", legacy)
		}
	}
	if runFlags.fs.ShorthandLookup("t") != nil {
		t.Fatal("legacy prompt shorthand must not be registered")
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
			cfg := parseRunDisplayFlags(t, "--resume", "agent-resume", tc.flag)
			if cfg.SessionID != "agent-resume" || cfg.Focused != tc.wantFocused || cfg.NoShellSteps != tc.wantNoShell {
				t.Fatalf("resume config = %#v", cfg)
			}
		})
	}
}
