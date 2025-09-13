package cli

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/mct/internal/api"
	"github.com/tursomari/machtiani/mct/internal/git"
	"github.com/tursomari/machtiani/mct/internal/utils"
)

// Build-time variable for system message frequency
var SystemMessageFrequencyHours = "24" // Default 24 hours, will be set via ldflags

func Execute() {

	// Handle help variants immediately (before any network/system-message work)
	if len(os.Args) >= 2 {
		cmd := os.Args[1]
		if cmd == "help" || cmd == "--help" || cmd == "-h" {
			printHelp()
			return
		}
	}
	sessionID := uuid.New().String()
	os.Setenv("MACHTIANI_SESSION_ID", sessionID) // Keep this for local CLI context
	// First, check if we're in answer-only mode early
	isAnswerOnlyMode := utils.IsAnswerOnlyMode()

	// Parse the system message frequency
	frequencyHours, err := strconv.Atoi(SystemMessageFrequencyHours)
	if err != nil {
		frequencyHours = 24 // Default to 24 hours if parsing fails
		utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Warning: failed to parse system message frequency, using default 24 hours: %v", err)
	}

	// Check if we should display the system message
	shouldDisplay, err := git.ShouldDisplaySystemMessage(frequencyHours)
	if err != nil {
		utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Warning: error checking if system message should be displayed: %v", err)
		// Default to displaying the message if there's an error
		shouldDisplay = true
	}

	if shouldDisplay && !isAnswerOnlyMode {
		// Try to fetch and display the system message
		systemMsg, err := git.GetLatestMachtianiSystemMessage(api.MachtianiGitRemoteURL)
		if err == nil && systemMsg != "" {
			// Check if the message is different from the last one shown
			lastMsg, err := git.GetLastSystemMessage()
			if err != nil {
				utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "Warning: failed to read last system message: %v", err)
				lastMsg = "" // Continue with displaying the message
			}

			// Only show if the message is different
			if systemMsg != lastMsg {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\n============= SYSTEM MESSAGE =============\n%s\n=========================================\n\n", systemMsg)

				// Save the new message as the last shown
				if err := git.SaveSystemMessage(systemMsg); err != nil {
					utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Warning: failed to save system message")
				}
			}

			// Record that we checked the message, regardless of whether we display it
			if err := git.RecordSystemMessageDisplayed(); err != nil {
				utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Warning: failed to record system message display time")
			}
		} else if err != nil {
			// Log the error but don't show it to the user
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Failed to fetch system message")
		}
	}

	config, err := utils.LoadConfig()
	if err != nil {
		utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error loading config")
		os.Exit(1)
	}

	// Require explicit subcommand
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	command := os.Args[1]

	// Handle help variants upfront
	if command == "help" || command == "--help" || command == "-h" {
		printHelp()
		return
	}

	// Validate required configuration based on subcommand
	// For 'prompt', only require model config; other commands also require server URLs.
	switch command {
	case "status":
		// Check full server requirements
		{
			missingRequired := false
			var missingFields []string
            if config.Environment.ModelAPIKey == "" {
                missingFields = append(missingFields, "OPENAI_API_KEY")
                missingRequired = true
            }
            if config.Environment.ModelBaseURL == "" {
                missingFields = append(missingFields, "OPENAI_BASE_URL")
                missingRequired = true
            }
			if api.MachtianiURL == "" {
				missingFields = append(missingFields, "MACHTIANI_URL")
				missingRequired = true
			}
			if api.RepoManagerURL == "" {
				missingFields = append(missingFields, "MACHTIANI_REPO_MANAGER_URL")
				missingRequired = true
			}
			if missingRequired {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Missing required configuration.\n")
				for _, field := range missingFields {
                    if field == "OPENAI_API_KEY" {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\nPlease set OPENAI_API_KEY with your provider API key, such as:\n\n$ export OPENAI_API_KEY=sk...\n\n")
                    } else if field == "OPENAI_BASE_URL" {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\nPlease set OPENAI_BASE_URL for your API provider, e.g.:\n\n$ export OPENAI_BASE_URL=\"https://api.openai.com/v1\"\n")
                    } else {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "  - %s (environment variable or config file entry)\n", field)
                    }
                }
                os.Exit(1)
            }
        }
		// Resolve API key early, used by connectivity checks after per-command parsing
		var apiKey *string = utils.GetCodeHostAPIKey(config)
		compatible, message, err := api.GetInstallInfo()
		if err != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error getting install info")
			os.Exit(1)
		}
		if !compatible {
			utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "This CLI is no longer compatible with the current environment. Please update to the latest version by following the below instructions\n\n%v", message)
			os.Exit(1)
		}
		// status: parse flags first, then resolve remote and run
		fs := pflag.NewFlagSet("status", pflag.ContinueOnError)
		remoteName := fs.String("remote", "origin", "Name of the remote repository")
		if err := fs.Parse(os.Args[2:]); err != nil {
			printHelp()
			os.Exit(1)
		}
		// Reject unexpected positional args
		if len(fs.Args()) > 0 {
			utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: unexpected positional arguments for 'status': %v\n", fs.Args())
			os.Exit(1)
		}
		remoteURL, err := git.GetRemoteURL(remoteName)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Git remote '%s' is not set.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Please set up a remote with:\n")
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "  git remote add %s <repository-url>\n", *remoteName)
				os.Exit(1)
			}
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error getting remote url")
			os.Exit(1)
		}
		// Connectivity check
		if ok, requiresAuth, err := git.CheckRemoteConnectivity(*remoteName, apiKey); err != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error checking remote connectivity")
			os.Exit(1)
		} else if !ok {
			if requiresAuth {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Authentication required for remote '%s'.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Set CODEHOST_API_KEY environment variable:\n")
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "     export CODEHOST_API_KEY=github_api_token_or_other\n")
			} else {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Cannot connect to remote '%s'.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Please check your internet connection and that the remote URL is correct.\n")
			}
			os.Exit(1)
		}
		utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Using remote URL: %s\n", remoteURL)
		handleStatus(&config, remoteURL)
		return

	case "sync":
		// Check full server requirements
		{
			missingRequired := false
			var missingFields []string
            if config.Environment.ModelAPIKey == "" {
                missingFields = append(missingFields, "OPENAI_API_KEY")
                missingRequired = true
            }
            if config.Environment.ModelBaseURL == "" {
                missingFields = append(missingFields, "OPENAI_BASE_URL")
                missingRequired = true
            }
			if api.MachtianiURL == "" {
				missingFields = append(missingFields, "MACHTIANI_URL")
				missingRequired = true
			}
			if api.RepoManagerURL == "" {
				missingFields = append(missingFields, "MACHTIANI_REPO_MANAGER_URL")
				missingRequired = true
			}
			if missingRequired {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Missing required configuration.\n")
				for _, field := range missingFields {
                    if field == "OPENAI_API_KEY" {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\nPlease set OPENAI_API_KEY with your provider API key, such as:\n\n$ export OPENAI_API_KEY=sk...\n\n")
                    } else if field == "OPENAI_BASE_URL" {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\nPlease set OPENAI_BASE_URL for your API provider, e.g.:\n\n$ export OPENAI_BASE_URL=\"https://api.openai.com/v1\"\n")
                    } else {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "  - %s (environment variable or config file entry)\n", field)
                    }
                }
                os.Exit(1)
            }
        }
		var apiKey *string = utils.GetCodeHostAPIKey(config)
		compatible, message, err := api.GetInstallInfo()
		if err != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error getting install info")
			os.Exit(1)
		}
		if !compatible {
			utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "This CLI is no longer compatible with the current environment. Please update to the latest version by following the below instructions\n\n%v", message)
			os.Exit(1)
		}
		// sync: use dedicated flag set and strict parsing
		fs := pflag.NewFlagSet("sync", pflag.ContinueOnError)
		remoteName := fs.String("remote", "origin", "Name of the remote repository")
		forceFlag := fs.Bool("force", false, "Skip confirmation prompt and proceed with the operation.")
		verboseFlag := fs.Bool("verbose", false, "Print verbose output including debug information.")
		costFlag := fs.Bool("cost", false, "Estimate token cost before proceeding with the sync.")
		costOnlyFlag := fs.Bool("cost-only", false, "Estimate token cost and exit without performing the sync.")
		amplifyFlag := fs.String("amplify", "off", "Amplification level (off, low, mid, high)")
		depthFlag := fs.Int("depth", 10000, "Depth of commit history to fetch (integer, default 10000)")
		modelFlag := fs.String("model", "", "Specify the model to use for this operation")
		modelThreadsFlag := fs.Int("model-threads", 0, "Number of threads for model processing (0 means use default)")

		if err := fs.Parse(os.Args[2:]); err != nil {
			utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: %v\n", err)
			fs.Usage()
			os.Exit(1)
		}
		// Reject unexpected positional args
		if len(fs.Args()) > 0 {
			utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: unexpected positional arguments for 'sync': %v\n", fs.Args())
			os.Exit(1)
		}

		// Validate amplify and depth
		if err := utils.ValidateAmplifyFlag(*amplifyFlag); err != nil {
			utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "%v\n", err)
			os.Exit(1)
		}
		if err := utils.ValidateDepthFlag(*depthFlag); err != nil {
			utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "%v\n", err)
			os.Exit(1)
		}

		// Resolve remote after parsing flags
		remoteURL, err := git.GetRemoteURL(remoteName)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Git remote '%s' is not set.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Please set up a remote with:\n")
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "  git remote add %s <repository-url>\n", *remoteName)
				os.Exit(1)
			}
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error getting remote url")
			os.Exit(1)
		}
		if ok, requiresAuth, err := git.CheckRemoteConnectivity(*remoteName, apiKey); err != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error checking remote connectivity")
			os.Exit(1)
		} else if !ok {
			if requiresAuth {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Authentication required for remote '%s'.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Set CODEHOST_API_KEY environment variable:\n")
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "     export CODEHOST_API_KEY=github_api_token_or_other\n")
			} else {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Cannot connect to remote '%s'.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Please check your internet connection and that the remote URL is correct.\n")
			}
			os.Exit(1)
		}
		utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Using remote URL: %s\n", remoteURL)

		headCommitHash, err := git.GetHeadCommitHash()
		if err != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error getting HEAD commit hash")
		}
		if err := handleSync(remoteURL, apiKey, *forceFlag, *verboseFlag, *costFlag, *costOnlyFlag, config, headCommitHash, *amplifyFlag, *depthFlag, *modelFlag, *modelThreadsFlag); err != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error handling sync")
			os.Exit(1)
		}
		return

	case "remove":
		// Check full server requirements
		{
			missingRequired := false
			var missingFields []string
            if config.Environment.ModelAPIKey == "" {
                missingFields = append(missingFields, "OPENAI_API_KEY")
                missingRequired = true
            }
            if config.Environment.ModelBaseURL == "" {
                missingFields = append(missingFields, "OPENAI_BASE_URL")
                missingRequired = true
            }
			if api.MachtianiURL == "" {
				missingFields = append(missingFields, "MACHTIANI_URL")
				missingRequired = true
			}
			if api.RepoManagerURL == "" {
				missingFields = append(missingFields, "MACHTIANI_REPO_MANAGER_URL")
				missingRequired = true
			}
			if missingRequired {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Missing required configuration.\n")
				for _, field := range missingFields {
                    if field == "OPENAI_API_KEY" {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\nPlease set OPENAI_API_KEY with your provider API key, such as:\n\n$ export OPENAI_API_KEY=sk...\n\n")
                    } else if field == "OPENAI_BASE_URL" {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\nPlease set OPENAI_BASE_URL for your API provider, e.g.:\n\n$ export OPENAI_BASE_URL=\"https://api.openai.com/v1\"\n")
                    } else {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "  - %s (environment variable or config file entry)\n", field)
                    }
                }
                os.Exit(1)
            }
        }
		var apiKey *string = utils.GetCodeHostAPIKey(config)
		compatible, message, err := api.GetInstallInfo()
		if err != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error getting install info")
			os.Exit(1)
		}
		if !compatible {
			utils.LogIfNotAnswerOnly(isAnswerOnlyMode, "This CLI is no longer compatible with the current environment. Please update to the latest version by following the below instructions\n\n%v", message)
			os.Exit(1)
		}
		fs := pflag.NewFlagSet("remove", pflag.ContinueOnError)
		remoteName := fs.String("remote", "origin", "Name of the remote repository")
		forceFlag := fs.Bool("force", false, "Skip confirmation prompt and proceed with the operation.")
		if err := fs.Parse(os.Args[2:]); err != nil {
			utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: %v\n", err)
			fs.Usage()
			os.Exit(1)
		}
		if len(fs.Args()) > 0 {
			utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: unexpected positional arguments for 'remove': %v\n", fs.Args())
			os.Exit(1)
		}
		remoteURL, err := git.GetRemoteURL(remoteName)
		if err != nil {
			if strings.Contains(err.Error(), "not found") {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Git remote '%s' is not set.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Please set up a remote with:\n")
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "  git remote add %s <repository-url>\n", *remoteName)
				os.Exit(1)
			}
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error getting remote url")
			os.Exit(1)
		}
		if ok, requiresAuth, err := git.CheckRemoteConnectivity(*remoteName, apiKey); err != nil {
			utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error checking remote connectivity")
			os.Exit(1)
		} else if !ok {
			if requiresAuth {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Authentication required for remote '%s'.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Set CODEHOST_API_KEY environment variable:\n")
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "     export CODEHOST_API_KEY=github_api_token_or_other\n")
			} else {
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Cannot connect to remote '%s'.\n", *remoteName)
				utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Please check your internet connection and that the remote URL is correct.\n")
			}
			os.Exit(1)
		}
		utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Using remote URL: %s\n", remoteURL)
		projectName := remoteURL
		vcsType := "git"
		handleRemove(remoteURL, projectName, vcsType, apiKey, *forceFlag, config)
		return
	case "help":
		printHelp()
		return // Exit after printing help

	default:
		if command == "prompt" {
			// Only require model configuration for prompt
			missingRequired := false
			var missingFields []string
            if config.Environment.ModelAPIKey == "" {
                missingFields = append(missingFields, "OPENAI_API_KEY")
                missingRequired = true
            }
            if config.Environment.ModelBaseURL == "" {
                missingFields = append(missingFields, "OPENAI_BASE_URL")
                missingRequired = true
            }
            if missingRequired {
                utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: Missing required configuration.\n")
                for _, field := range missingFields {
                    if field == "OPENAI_API_KEY" {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\nPlease set OPENAI_API_KEY with your provider API key, such as:\n\n$ export OPENAI_API_KEY=sk...\n\n")
                    } else if field == "OPENAI_BASE_URL" {
                        utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "\nPlease set OPENAI_BASE_URL for your API provider, e.g.:\n\n$ export OPENAI_BASE_URL=\"https://api.openai.com/v1\"\n")
                    }
                }
                os.Exit(1)
            }
            startTime := time.Now()
			args := os.Args[2:]
			headCommitHash, err := git.GetHeadCommitHash()
			if err != nil {
				utils.LogErrorIfNotAnswerOnly(isAnswerOnlyMode, err, "Error getting HEAD commit hash")
			}
			// apiKey only needed for git remote checks in other commands; pass nil here
			handlePrompt(args, &config, nil, headCommitHash)
			duration := time.Since(startTime)
			utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Total response handling took %s\n", duration)
			return
		}

		// Unknown subcommand: error + help, non-zero exit
		utils.PrintIfNotAnswerOnly(isAnswerOnlyMode, "Error: unknown command '%s'\n\n", command)
		printHelp()
		os.Exit(1)
	}
}
