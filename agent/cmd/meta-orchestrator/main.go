package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/spf13/pflag"

	"github.com/tursomari/machtiani/agent/internal/orchestrator"
)

func generateSessionID() string {
	now := time.Now().UTC().Format("20060102T150405")
	return fmt.Sprintf("agent-%s-%04d", now, rand.Intn(10000))
}

func main() {
	mode := pflag.String("mode", "", "Operating mode (required)")
	model := pflag.String("model", "", "Model alias (optional)")
	shellAgentModel := pflag.String("shell-agent-model", "", "Model alias for shell-agent subprocesses (optional)")
	instruction := pflag.StringP("instruction", "f", "", "Path to instruction file (required)")
	tag := pflag.String("tag", "now", "Tag suffix for answer and command tags (optional)")
	persistTmpData := pflag.Bool("persist-tmp-data", false, "Keep temporary data after execution (optional)")

	pflag.Parse()

	if *mode == "" {
		fmt.Fprintln(os.Stderr, "Error: --mode is required")
		pflag.Usage()
		os.Exit(1)
	}

	if *instruction == "" {
		fmt.Fprintln(os.Stderr, "Error: --instruction (-f) is required")
		pflag.Usage()
		os.Exit(1)
	}

	sessionID := generateSessionID()

	fmt.Printf("Starting meta-orchestrator session %s in mode %s\n", sessionID, *mode)

	ctx := context.Background()
	exitCode, err := orchestrator.RunLoop(ctx, sessionID, *instruction, *mode, *model, *shellAgentModel, *tag, *persistTmpData)
	if err != nil {
		log.Printf("Error in run loop: %v", err)
		os.Exit(1)
	}
	if exitCode != 0 {
		fmt.Printf("Meta-orchestrator session %s ended with exit code %d\n", sessionID, exitCode)
		os.Exit(exitCode)
	}
	fmt.Printf("Meta-orchestrator session %s completed successfully. Final answer is ready.\n", sessionID)
}
