package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestAuthLoginUsesInteractiveModelHostCLI(t *testing.T) {
	root := t.TempDir()
	capture := filepath.Join(root, "args")
	command := filepath.Join(root, "model-host")
	script := `#!/bin/sh
printf '%s' "$*" > "$MACHTIANI_AUTH_CAPTURE"
printf '%s\n' 'provider login completed'
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "private-profile.json")
	config := `default_model = "shared"

[providers.shared]
transport = "model-host"
profile = ` + jsonQuote(profile) + `
command = ` + jsonQuote(command) + `

[models.shared]
provider = "shared"
model = "provider-model"
context_length = 131072
`
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", configPath)
	t.Setenv("MACHTIANI_AUTH_CAPTURE", capture)
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)

	var code int
	stdout := captureStdout(t, func() {
		code = handleAuthCommand([]string{"login", "--model", "shared", "--mode", "device_code"})
	})
	if code != 0 || !strings.Contains(stdout, "provider login completed") {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
	args, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(args), "auth login --profile "+profile+" --mode device_code"; got != want {
		t.Fatalf("args=%q want=%q", got, want)
	}
}

func TestCallModelHostAuthReturnsStructuredStatus(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "model-host")
	script := `#!/bin/sh
read request
printf '%s\n' '{"v":1,"id":"auth","result":{"authenticated":true,"method":"api_key"}}'
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	result, protocolErr, err := callModelHostControl(command, filepath.Join(root, "profile.json"), "auth/status")
	if err != nil || protocolErr != nil || string(result) != `{"authenticated":true,"method":"api_key"}` {
		t.Fatalf("callModelHostControl = %s, %+v, %v", result, protocolErr, err)
	}
}

func TestCallModelHostAuthReturnsStructuredError(t *testing.T) {
	root := t.TempDir()
	command := filepath.Join(root, "model-host")
	script := `#!/bin/sh
read request
printf '%s\n' '{"v":1,"id":"auth","error":{"code":"AUTH_EXPIRED","message":"Sign in again."}}'
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	_, protocolErr, err := callModelHostControl(command, filepath.Join(root, "profile.json"), "auth/status")
	if err != nil || protocolErr == nil || protocolErr.Code != "AUTH_EXPIRED" || protocolErr.Message != "Sign in again." {
		t.Fatalf("callModelHostControl error = %+v, %v", protocolErr, err)
	}
}
