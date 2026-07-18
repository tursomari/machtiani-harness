package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	updatepkg "github.com/tursomari/machtiani/agent/internal/update"
	"golang.org/x/term"
)

type updateCommandManager interface {
	Check(context.Context) (updatepkg.Result, error)
	Update(context.Context, updatepkg.Result) (updatepkg.Result, error)
	Install(context.Context, string, string) (updatepkg.Receipt, error)
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
		description: "Check and install Nix-managed mct-agent updates",
		handler:     handleUpdateCommand,
	})
	cliCommands = append(cliCommands, cliCommand{
		name:        "install",
		description: "Install mct-agent through a dedicated Nix profile",
		handler:     handleInstallCommand,
	})
}

func handleUpdateCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent update", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	checkOnly := fs.Bool("check", false, "check for an update without installing it")
	yes := fs.Bool("yes", false, "install an available update without prompting")
	noInteractive := fs.Bool("no-interactive", false, "never prompt; check only unless combined with --yes")
	jsonOutput := fs.Bool("json", false, "print one machine-readable JSON result")
	_ = fs.Bool("verbose", false, "show detailed Nix and Git output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent update [--check] [--yes] [--no-interactive] [--json]")
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

func handleInstallCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent install", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	source := fs.String("source", "", "clean source checkout")
	home, _ := os.UserHomeDir()
	prefix := fs.String("prefix", filepath.Join(home, ".local"), "installation prefix")
	noInteractive := fs.Bool("no-interactive", false, "never prompt; use --prefix or the default prefix")
	_ = fs.Bool("verbose", false, "show detailed Nix and Git output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent install --source <checkout> [--prefix <dir>] [--no-interactive] [--verbose]")
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
	selectedPrefix, err := resolveInstallPrefix(
		*prefix,
		home,
		fs.Changed("prefix"),
		*noInteractive,
		term.IsTerminal(int(os.Stdin.Fd())),
		bufio.NewReader(os.Stdin),
		os.Stderr,
		directoryExists,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: choose installation location:", err)
		return 1
	}
	if _, err := updateManagerFactory().Install(context.Background(), *source, selectedPrefix); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	fmt.Fprintln(os.Stderr, "Managed mct-agent installation complete.")
	return 0
}

func resolveInstallPrefix(prefix, home string, prefixExplicit, noInteractive, interactive bool, reader *bufio.Reader, out io.Writer, isDir func(string) bool) (string, error) {
	if prefixExplicit || noInteractive || !interactive {
		return prefix, nil
	}
	return promptInstallPrefix(reader, out, availableInstallPrefixes(prefix, home, isDir), home)
}

// availableInstallPrefixes returns the default followed by at most two common
// prefixes whose bin directories already exist. The installer can create a
// missing bin directory for the selected prefix, but existing locations make
// more useful alternatives in the prompt.
func availableInstallPrefixes(defaultPrefix, home string, isDir func(string) bool) []string {
	prefixes := []string{defaultPrefix}
	candidates := []struct {
		prefix string
		probe  string
	}{
		{prefix: home, probe: filepath.Join(home, "bin")},
		{prefix: "/opt/homebrew", probe: filepath.Join("/opt/homebrew", "bin")},
		{prefix: "/usr/local", probe: filepath.Join("/usr/local", "bin")},
	}
	for _, candidate := range candidates {
		if len(prefixes) == 3 {
			break
		}
		if candidate.prefix == "" || filepath.Clean(candidate.prefix) == filepath.Clean(defaultPrefix) || !isDir(candidate.probe) {
			continue
		}
		prefixes = append(prefixes, candidate.prefix)
	}
	return prefixes
}

func promptInstallPrefix(reader *bufio.Reader, out io.Writer, prefixes []string, home string) (string, error) {
	if len(prefixes) == 0 {
		return "", errors.New("no installation prefixes available")
	}
	fmt.Fprintln(out, "Choose where to install mct-agent:")
	for index, prefix := range prefixes {
		destination := displayInstallPath(filepath.Join(prefix, "bin", "mct-agent"), home)
		suffix := ""
		if index == 0 {
			suffix = " (recommended)"
		}
		fmt.Fprintf(out, "  %d) %s%s\n", index+1, destination, suffix)
	}
	customIndex := len(prefixes) + 1
	fmt.Fprintf(out, "  %d) Custom prefix\n", customIndex)

	for {
		fmt.Fprint(out, "Selection [1]: ")
		value, err := readInitLine(reader)
		if err != nil {
			return "", err
		}
		if value == "" {
			return prefixes[0], nil
		}
		selection, err := strconv.Atoi(value)
		if err != nil || selection < 1 || selection > customIndex {
			fmt.Fprintf(out, "Enter a number from 1 to %d.\n", customIndex)
			continue
		}
		if selection <= len(prefixes) {
			return prefixes[selection-1], nil
		}
		for {
			fmt.Fprint(out, "Custom installation prefix (binary goes in <prefix>/bin): ")
			custom, err := readInitLine(reader)
			if err != nil {
				return "", err
			}
			if custom == "" {
				fmt.Fprintln(out, "Installation prefix cannot be empty.")
				continue
			}
			return cleanInstallPrefix(custom, home), nil
		}
	}
}

func cleanInstallPrefix(prefix, home string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "~" {
		return home
	}
	if strings.HasPrefix(prefix, "~/") {
		return filepath.Join(home, strings.TrimPrefix(prefix, "~/"))
	}
	return filepath.Clean(prefix)
}

func displayInstallPath(path, home string) string {
	cleanHome := filepath.Clean(home)
	cleanPath := filepath.Clean(path)
	if cleanPath == cleanHome {
		return "~"
	}
	if home != "" && strings.HasPrefix(cleanPath, cleanHome+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(cleanPath, cleanHome)
	}
	return cleanPath
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
