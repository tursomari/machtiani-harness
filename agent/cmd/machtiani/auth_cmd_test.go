package main

import (
	"os"
	"path/filepath"
	"testing"
)

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
	result, protocolErr, err := callModelHostAuth(command, filepath.Join(root, "profile.json"), "auth/status")
	if err != nil || protocolErr != nil || string(result) != `{"authenticated":true,"method":"api_key"}` {
		t.Fatalf("callModelHostAuth = %s, %+v, %v", result, protocolErr, err)
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
	_, protocolErr, err := callModelHostAuth(command, filepath.Join(root, "profile.json"), "auth/status")
	if err != nil || protocolErr == nil || protocolErr.Code != "AUTH_EXPIRED" || protocolErr.Message != "Sign in again." {
		t.Fatalf("callModelHostAuth error = %+v, %v", protocolErr, err)
	}
}
