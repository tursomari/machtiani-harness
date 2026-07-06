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

func generateSessionIDs() (string, string) {
	now := time.Now().UTC().Format("20060102T150405")
	suffix := rand.Intn(10000)
	return fmt.Sprintf("meta-%s-%04d", now, suffix), fmt.Sprintf("agent-%s-%04d", now, suffix)
}

func main() {
	mode := pflag.String("mode", "", "Operating mode (required)")
	model := pflag.String("model", "", "Model alias (optional)")
	shellAgentModel := pflag.String("shell-agent-model", "", "Model alias for shell-agent subprocesses (optional)")
	instruction := pflag.StringP("instruction", "f", "", "Path to instruction file (required)")
	tag := pflag.String("tag", "now", "Tag suffix for answer and command tags (optional)")
	persistTmpData := pflag.Bool("persist-tmp-data", false, "Keep temporary data after execution (optional)")
	reviewMode := pflag.Bool("review-mode", false, "Operate in peer review mode with review-specific classifier prompt (optional)")

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

	metaSessionID, mctSessionID := generateSessionIDs()

	fmt.Printf("Meta session: %s, MCT session: %s, mode: %s\n", metaSessionID, mctSessionID, *mode)

	ctx := context.Background()
	exitCode, err := orchestrator.RunLoop(ctx, metaSessionID, mctSessionID, *instruction, *mode, *model, *shellAgentModel, *tag, *persistTmpData, *reviewMode)
	if err != nil {
		log.Printf("Error in run loop: %v", err)
		os.Exit(1)
	}
	if exitCode != 0 {
		fmt.Printf("Meta-orchestrator session %s ended with exit code %d\n", metaSessionID, exitCode)
		os.Exit(exitCode)
	}
	fmt.Printf("Meta-orchestrator session %s completed successfully. Final answer is ready.\n", metaSessionID)
}
