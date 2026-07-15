package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/session"
	"golang.org/x/term"
)

func handleSessionPruneCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent session prune", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "show disposable session data without removing it")
	yes := fs.Bool("yes", false, "confirm removal of the reported disposable data")
	noInteractive := fs.Bool("no-interactive", false, "never prompt; requires --yes unless --dry-run")
	jsonOutput := fs.Bool("json", false, "print the prune report as JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent session prune [session-id] [--dry-run] [--yes] [--no-interactive] [--json]")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Remove full LLM input logs, deprecated shell-agent state files, and their empty directories.")
		fmt.Fprintln(os.Stderr, "Omit session-id to inspect every inactive session in the current project store.")
		fmt.Fprintln(os.Stderr, "Use --dry-run to inspect changes. Automation must pass --no-interactive --yes.")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 {
		fs.Usage()
		return 2
	}
	sessionID := ""
	if fs.NArg() == 1 {
		sessionID = fs.Arg(0)
	}

	options := session.PruneOptions{SessionID: sessionID, DryRun: *dryRun}
	if *dryRun {
		report, err := session.PruneSessions(options)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Session prune dry-run failed:", err)
			return 1
		}
		printPruneReport(report, *jsonOutput)
		return 0
	}

	if !*yes {
		if *noInteractive || !term.IsTerminal(int(os.Stdin.Fd())) {
			fmt.Fprintln(os.Stderr, "session prune requires confirmation; pass --yes (and --no-interactive for automation) or use --dry-run")
			return 2
		}
		preview, err := session.PruneSessions(session.PruneOptions{SessionID: sessionID, DryRun: true})
		if err != nil {
			fmt.Fprintln(os.Stderr, "Session prune preview failed:", err)
			return 1
		}
		printPruneReport(preview, false)
		confirmed, err := promptYesNo(bufio.NewReader(os.Stdin), os.Stdout, "Remove the reported disposable session data? [y/N]: ", false)
		if err != nil {
			fmt.Fprintln(os.Stderr, "Session prune confirmation failed:", err)
			return 1
		}
		if !confirmed {
			fmt.Fprintln(os.Stdout, "Session prune cancelled.")
			return 0
		}
	}

	report, err := session.PruneSessions(session.PruneOptions{SessionID: sessionID})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Session prune failed:", err)
		return 1
	}
	printPruneReport(report, *jsonOutput)
	return 0
}

func printPruneReport(report session.PruneReport, jsonOutput bool) {
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(os.Stdout, string(data))
		return
	}
	action := "Removed"
	header := "Session prune complete"
	if report.DryRun {
		action = "Would remove"
		header = "Session prune dry run"
	}
	fmt.Fprintf(os.Stdout, "%s\nRoot: %s\n", header, report.Root)
	if report.RequestedSession != "" {
		fmt.Fprintf(os.Stdout, "Session: %s\n", report.RequestedSession)
	}
	fmt.Fprintf(os.Stdout, "Sessions scanned: %d\nSessions changed: %d\n", report.SessionsScanned, report.SessionsChanged)
	fmt.Fprintf(os.Stdout, "%s LLM input logs: %d files, %s\n", action, report.RemovedLLMInputFiles, formatMigrationBytes(report.RemovedLLMInputBytes))
	fmt.Fprintf(os.Stdout, "%s deprecated shell-agent state: %d files, %s\n", action, report.RemovedShellAgentStateFiles, formatMigrationBytes(report.RemovedShellAgentStateBytes))
	fmt.Fprintf(os.Stdout, "%s empty directories: %d\n", action, report.RemovedDirectories)
	if len(report.SkippedActiveSessions) > 0 {
		fmt.Fprintf(os.Stdout, "Skipped active sessions: %s\n", strings.Join(report.SkippedActiveSessions, ", "))
	}
}
