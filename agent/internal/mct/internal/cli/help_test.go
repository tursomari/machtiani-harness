package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestRegisterPromptFlagsAllPresent(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	registerPromptFlags(fs)

	expectedFlags := []string{
		"file", "model", "orch-model", "answer-model",
		"openai-model", "openai-api-key", "openai-base-url",
		"param", "param-json", "agent-model",
		"session", "match-strength", "mode",
		"include-history", "max-input-tokens",
		"verbose", "shell-agent",
	}
	for _, name := range expectedFlags {
		f := fs.Lookup(name)
		if f == nil {
			t.Errorf("flag %q not registered", name)
		}
	}
}

func TestVerboseShorthand(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	registerPromptFlags(fs)

	f := fs.Lookup("verbose")
	if f == nil {
		t.Fatal("verbose flag not found")
	}
	if f.Shorthand != "v" {
		t.Fatalf("expected verbose shorthand 'v', got %q", f.Shorthand)
	}
}

func TestHiddenFlagsNotInUsage(t *testing.T) {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	registerPromptFlags(fs)

	// Flags that should be hidden from --help
	hiddenFlags := []string{"openai-model", "openai-api-key", "openai-base-url", "include-history"}
	for _, name := range hiddenFlags {
		f := fs.Lookup(name)
		if f == nil {
			t.Errorf("hidden flag %q not registered", name)
			continue
		}
		if !f.Hidden {
			t.Errorf("flag %q should be Hidden but isn't", name)
		}
	}

	// Verify hidden flags don't appear in usage output
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	usage := buf.String()
	for _, name := range hiddenFlags {
		if strings.Contains(usage, "--"+name) {
			t.Errorf("hidden flag --%s should not appear in usage output", name)
		}
	}
}

func TestVisibleFlagsInUsage(t *testing.T) {
	fs := pflag.NewFlagSet("mct", pflag.ContinueOnError)
	registerPromptFlags(fs)

	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.PrintDefaults()
	output := buf.String()

	if !strings.Contains(output, "--file") {
		t.Error("expected --file in flag usage output")
	}
	if !strings.Contains(output, "--verbose") {
		t.Error("expected --verbose in flag usage output")
	}
}
