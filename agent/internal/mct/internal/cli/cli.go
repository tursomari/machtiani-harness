package cli

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/git"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/readme"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/utils"
)

// Build-time variables for system message behavior
var (
	SystemMessageFrequencyHours = "24"                                     // Default 24 hours, overridden via ldflags
	SystemMessageRemote         = "https://github.com/tursomari/machtiani" // Remote repository for system messages
)

func Execute() {
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "--version", "-version":
			printVersion()
			return
		case "help", "--help", "-h":
			printHelp()
			return
		}
	}

	sessionID := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_ID"))
	if sessionID == "" {
		sessionID = uuid.New().String()
		os.Setenv("MACHTIANI_SESSION_ID", sessionID)
	}

	isAnswerOnlyMode := utils.IsAnswerOnlyMode()
	skipReadmeManager := strings.TrimSpace(os.Getenv(readme.SkipReadmeManagerEnv)) != ""
	verboseRequested := hasVerboseFlag(os.Args[1:])

	frequencyHours, err := strconv.Atoi(SystemMessageFrequencyHours)
	if err != nil {
		frequencyHours = 24
		utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Warning: failed to parse system message frequency, using default 24 hours: %v", err)
	}

	shouldDisplay, err := git.ShouldDisplaySystemMessage(frequencyHours)
	if err != nil {
		utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Warning: error checking if system message should be displayed: %v", err)
		shouldDisplay = true
	}

	if shouldDisplay && !isAnswerOnlyMode {
		systemMsg, fetchErr := git.GetLatestMachtianiSystemMessage(SystemMessageRemote)
		if fetchErr == nil && systemMsg != "" {
			lastMsg, readErr := git.GetLastSystemMessage()
			if readErr != nil {
				utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Warning: failed to read last system message: %v", readErr)
				lastMsg = ""
			}
			if systemMsg != lastMsg {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\n============= SYSTEM MESSAGE =============\n%s\n=========================================\n\n", systemMsg)
				if err := git.SaveSystemMessage(systemMsg); err != nil {
					utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Warning: failed to save system message")
				}
			}
			if err := git.RecordSystemMessageDisplayed(); err != nil {
				utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Warning: failed to record system message display time")
			}
		} else if fetchErr != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, fetchErr, "Failed to fetch system message")
		}
	}

	if !skipReadmeManager {
		if headCommitHash, err := git.GetHeadCommitHash(); err == nil {
			if mgr, mgrErr := readme.NewManager(isAnswerOnlyMode, verboseRequested); mgrErr == nil {
				ctx := context.Background()
				if runErr := mgr.Run(ctx, headCommitHash); runErr != nil {
					utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, runErr, "Internal README management failed")
				}
			} else if !strings.Contains(strings.ToLower(mgrErr.Error()), "not a git repository") {
				utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, mgrErr, "Failed to initialize internal README manager")
			}
		} else if err != nil && !strings.Contains(strings.ToLower(err.Error()), "not a git repository") {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Failed to determine project commit for README management")
		}
	}

	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	command := os.Args[1]
	if command == "help" || command == "--help" || command == "-h" {
		printHelp()
		return
	}

	switch command {
	case "prompt":
		startTime := time.Now()
		args := os.Args[2:]
		handlePrompt(args)
		duration := time.Since(startTime)
		utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Total response handling took %s\n", duration)
		return

	default:
		utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: unknown command '%s'\n\n", command)
		printHelp()
		os.Exit(1)
	}
}

func hasVerboseFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--verbose" || strings.HasPrefix(arg, "--verbose=") {
			return true
		}
	}
	return false
}
