package discovery

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

// TestSync_StdinSeeding verifies that RG_OUT blocks in stdin populate seenPaths,
// allowing subsequent read_file calls to succeed without an explicit RG step.
func TestSync_StdinSeeding(t *testing.T) {
	// 1. Setup a temporary file to "read"
	tmpDir := t.TempDir()
	cwd, _ := os.Getwd()
	defer os.Chdir(cwd)
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	targetFile := "newly_patched.go"
	if err := os.WriteFile(targetFile, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// 2. Mock stdin with RG_OUT seeding
	// We need to pipe this into the process's stdin, but Run() reads from os.Stdin.
	// So we must pipe it.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	seededContent := fmt.Sprintf("RG_OUT:\n%s\nEND_RG_OUT\n", targetFile)
	go func() {
		_, _ = w.Write([]byte(seededContent))
		_ = w.Close()
	}()
	oldStdin := os.Stdin
	defer func() { os.Stdin = oldStdin }()
	os.Stdin = r

	// 3. Mock chatInvoker to return a read_file command for the seeded file
	// followed by a final block to exit cleanly.
	originalInvoker := chatInvoker
	defer func() { chatInvoker = originalInvoker }()

	turn := 0
	chatInvoker = func(ctx context.Context, cfg LLMSettings, msgs []chatMessage) (string, error) {
		turn++
		if turn == 1 {
			// First turn: try to read the file.
			// If seenPaths is NOT populated, this will be rejected by the logic in Run()
			// and the loop will continue (or we'll see a rejection message in the next turn).
			return fmt.Sprintf(`{"tool": "read_file", "args": {"path": "%s", "program": "1,10p"}}`, targetFile), nil
		}
		// Second turn: emit final block to exit
		return fmt.Sprintf("BEGIN_RELEVANT_FILES[file-discovery]\n%s\nEND_RELEVANT_FILES[file-discovery]", targetFile), nil
	}

	// 4. Run discovery
	// We need to capture stderr/stdout to verify behavior if needed, but the exit code 0
	// and the fact that we reach turn 2 implies success.
	// Ideally we'd check if the read_file actually ran.
	// We can inspect the messages passed to the second turn to see if they contain SED_OUT.

	// Refine mock: check messages in turn 2
	sedOutSeen := false
	chatInvoker = func(ctx context.Context, cfg LLMSettings, msgs []chatMessage) (string, error) {
		turn++
		if turn == 1 {
			return fmt.Sprintf(`{"tool": "read_file", "args": {"path": "%s", "program": "1,10p"}}`, targetFile), nil
		}
		// Check previous messages for SED_OUT
		for _, m := range msgs {
			if strings.Contains(m.Content, "SED_OUT["+targetFile+"]") {
				sedOutSeen = true
			}
			// If seenPaths was missing, we'd see a rejection message instead
			if strings.Contains(m.Content, "read_file path not allowed") {
				t.Errorf("read_file was rejected! seenPaths not populated?")
			}
		}
		return fmt.Sprintf("BEGIN_RELEVANT_FILES[file-discovery]\n%s\nEND_RELEVANT_FILES[file-discovery]", targetFile), nil
	}

	cfg := cfgpkg.Config{
		MaxRounds:     5,
		ToolCallMode:  "json",
		MaxTranscript: 1024 * 1024,
		CmdTimeoutSec: 30,
		// We need a dummy model config to pass validation
		Model: "test-model",
	}
	llmCfg := LLMSettings{
		Model: llm.ResolvedModel{Model: "test-model", APIKey: "dummy", BaseURL: "dummy"},
	}

	// We need sed in PATH for this to work, or mock runSed.
	// Assuming sed is available (linux environment).
	if _, err := exec.LookPath("sed"); err != nil {
		t.Skip("sed not found")
	}

	exitCode := Run(context.Background(), cfg, llmCfg)
	if exitCode != 0 {
		t.Fatalf("Run exited with code %d", exitCode)
	}

	if !sedOutSeen {
		t.Fatalf("Did not see SED_OUT in chat history; read_file failed or wasn't executed")
	}
}
