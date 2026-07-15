package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/pflag"
	updatepkg "github.com/tursomari/machtiani/agent/internal/update"
	"golang.org/x/term"
)

type updateCommandManager interface {
	Check(context.Context) (updatepkg.Result, error)
	Update(context.Context, updatepkg.Result) (updatepkg.Result, error)
	Register(context.Context, string, string) (updatepkg.Receipt, error)
}

var updateManagerFactory = func() updateCommandManager {
	home, _ := os.UserHomeDir()
	executable, _ := os.Executable()
	return updatepkg.NewManager(updatepkg.Options{
		Home:       home,
		Version:    Version,
		Commit:     Commit,
		Executable: executable,
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
}

func init() {
	cliCommands = append(cliCommands, cliCommand{
		name:        "update",
		description: "Check and install mct-agent source updates",
		handler:     handleUpdateCommand,
	})
}

func handleUpdateCommand(args []string) int {
	if len(args) > 0 && args[0] == "register" {
		return handleUpdateRegisterCommand(args[1:])
	}
	fs := pflag.NewFlagSet("mct-agent update", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	checkOnly := fs.Bool("check", false, "check for an update without installing it")
	yes := fs.Bool("yes", false, "install an available update without prompting")
	noInteractive := fs.Bool("no-interactive", false, "never prompt; check only unless combined with --yes")
	jsonOutput := fs.Bool("json", false, "print one machine-readable JSON result")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent update [--check] [--yes] [--no-interactive] [--json]")
		fmt.Fprintln(os.Stderr, "       mct-agent update register --source <dir> --prefix <dir>")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Error: mct-agent update takes flags, not positional arguments")
		return 2
	}
	manager := updateManagerFactory()
	result, err := manager.Check(context.Background())
	if err != nil {
		return printUpdateError(err, *jsonOutput)
	}
	shouldInstall := result.Status == updatepkg.StatusAvailable && *yes && !*checkOnly
	if result.Status == updatepkg.StatusAvailable && !*checkOnly && !*yes && !*noInteractive && !*jsonOutput && term.IsTerminal(int(os.Stdin.Fd())) {
		printUpdateSummary(os.Stderr, result)
		confirmed, promptErr := promptYesNo(bufio.NewReader(os.Stdin), os.Stderr, "Update now? [y/N]: ", false)
		if promptErr != nil {
			return printUpdateError(promptErr, false)
		}
		shouldInstall = confirmed
		if !confirmed {
			result.Status = updatepkg.StatusDeclined
		}
	}
	if shouldInstall {
		result, err = manager.Update(context.Background(), result)
		if err != nil {
			return printUpdateError(err, *jsonOutput)
		}
	}
	if *jsonOutput {
		data, err := json.Marshal(result)
		if err != nil {
			return printUpdateError(err, true)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return 0
	}
	printUpdateSummary(os.Stderr, result)
	return 0
}

func handleUpdateRegisterCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent update register", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	source := fs.String("source", "", "managed source checkout")
	prefix := fs.String("prefix", "", "installation prefix")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent update register --source <dir> --prefix <dir>")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 || strings.TrimSpace(*source) == "" || strings.TrimSpace(*prefix) == "" {
		fs.Usage()
		return 2
	}
	if _, err := updateManagerFactory().Register(context.Background(), *source, *prefix); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "Managed mct-agent installation registered.")
	return 0
}

func printUpdateSummary(out *os.File, result updatepkg.Result) {
	switch result.Status {
	case updatepkg.StatusCurrent:
		fmt.Fprintf(out, "mct-agent is current at %s.\n", shortSHA(result.CurrentCommit, 12))
	case updatepkg.StatusAvailable:
		fmt.Fprintf(out, "mct-agent update available: %s -> %s\n", shortSHA(result.CurrentCommit, 12), shortSHA(result.CandidateCommit, 12))
	case updatepkg.StatusUpdated:
		fmt.Fprintf(out, "mct-agent updated to %s.\n", shortSHA(result.CandidateCommit, 12))
	case updatepkg.StatusDeclined:
		fmt.Fprintln(out, "Update declined.")
	}
}

func printUpdateError(err error, jsonOutput bool) int {
	if jsonOutput {
		data, _ := json.Marshal(map[string]string{"status": "error", "error": err.Error()})
		fmt.Fprintln(os.Stdout, string(data))
	} else {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
	return 1
}

const updateReexecEnv = "MCT_AGENT_UPDATE_REEXEC"

func automaticUpdateEligible(args []string, stdinTTY, stdoutTTY, stderrTTY bool) bool {
	if !stdinTTY || !stdoutTTY || !stderrTTY || len(args) == 0 {
		return false
	}
	if args[0] == "update" || args[0] == "completion" {
		return false
	}
	for _, arg := range args {
		switch arg {
		case "--json", "--no-interactive", "--help", "-h", "--version", "-version":
			return false
		}
	}
	return true
}

// maybeAutomaticUpdate performs only soft startup maintenance. The explicit
// update command remains the place where updater failures are fatal.
func maybeAutomaticUpdate(args []string) (handled bool, code int) {
	if os.Getenv(updateReexecEnv) == "1" || !automaticUpdateEligible(args,
		term.IsTerminal(int(os.Stdin.Fd())),
		term.IsTerminal(int(os.Stdout.Fd())),
		term.IsTerminal(int(os.Stderr.Fd()))) {
		return false, 0
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, 0
	}
	manager := updatepkg.NewManager(updatepkg.Options{Home: home, Version: Version, Commit: Commit, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	if _, err := os.Stat(manager.Paths().Receipt); errors.Is(err, os.ErrNotExist) {
		return false, 0
	} else if err != nil {
		fmt.Fprintln(os.Stderr, "Update check skipped:", err)
		return false, 0
	}
	due, err := manager.Due(time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "Update check skipped:", err)
		return false, 0
	}
	if !due {
		return false, 0
	}
	cfg, err := manager.Config()
	if err != nil || cfg.Policy == updatepkg.PolicyOff {
		if err != nil {
			fmt.Fprintln(os.Stderr, "Update check skipped:", err)
		}
		return false, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	result, err := manager.Check(ctx)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "Update check failed; continuing with the installed binary:", err)
		return false, 0
	}
	if result.Status != updatepkg.StatusAvailable {
		return false, 0
	}
	printAutomaticUpdateDetails(result)
	if cfg.Policy == updatepkg.PolicyNotify {
		return false, 0
	}
	if cfg.Policy == updatepkg.PolicyPrompt {
		confirmed, err := promptYesNo(bufio.NewReader(os.Stdin), os.Stderr, "Update now? [y/N]: ", false)
		if err != nil || !confirmed {
			if err != nil {
				fmt.Fprintln(os.Stderr, "Update prompt failed; continuing:", err)
			}
			return false, 0
		}
	}
	result, err = manager.Update(context.Background(), result)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Update failed; continuing with the installed binary:", err)
		return false, 0
	}
	fmt.Fprintf(os.Stderr, "mct-agent updated to %s; continuing original command.\n", shortSHA(result.CandidateCommit, 12))
	child := exec.Command(result.BinaryPath, args...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = append(os.Environ(), updateReexecEnv+"=1")
	if err := child.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return true, exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "Unable to continue with updated mct-agent:", err)
		return true, 1
	}
	return true, 0
}

func printAutomaticUpdateDetails(result updatepkg.Result) {
	fmt.Fprintf(os.Stderr, "mct-agent update available: %s -> %s\n", shortSHA(result.CurrentCommit, 12), shortSHA(result.CandidateCommit, 12))
	fmt.Fprintf(os.Stderr, "Remote: %s (%s)\n", result.Remote, result.DefaultBranch)
	fmt.Fprintf(os.Stderr, "Source: %s\nInstall target: %s\n", result.SourceDir, result.BinaryPath)
	fmt.Fprintln(os.Stderr, "The update will be built locally from the managed source checkout.")
}
