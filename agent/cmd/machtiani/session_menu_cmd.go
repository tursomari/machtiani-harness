package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/presentation"
)

func handleSessionMenuCommand(args []string) int {
	fs := pflag.NewFlagSet("machtiani session menu", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	noInteractive := fs.Bool("no-interactive", false, "never prompt; requires exactly one supplied action")
	list := fs.Bool("list", false, "list sessions without opening the menu")
	archiveID := fs.String("archive", "", "archive a session by ID without opening the menu")
	unarchiveID := fs.String("unarchive", "", "unarchive a session by ID without opening the menu")
	archiveSince := fs.String("archive-since", "", "archive sessions updated on or after this date")
	archiveUntil := fs.String("archive-until", "", "with --archive-since, include sessions through this date")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: machtiani session menu")
		fmt.Fprintln(os.Stderr, "       machtiani session menu --no-interactive --archive <session-id>")
		fmt.Fprintln(os.Stderr, "       machtiani session menu --no-interactive --unarchive <session-id>")
		fmt.Fprintln(os.Stderr, "       machtiani session menu --no-interactive --archive-since <date> [--archive-until <date>]")
		fmt.Fprintln(os.Stderr, "       machtiani session menu --no-interactive --list")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return 2
	}

	if err := requireInteractive(*noInteractive); err != nil {
		fmt.Fprintln(os.Stderr, "session menu requires a terminal; pass --no-interactive with --list, --archive, --unarchive, or --archive-since")
		return 2
	}
	if *noInteractive {
		return runSessionMenuNonInteractive(*list, *archiveID, *unarchiveID, *archiveSince, *archiveUntil)
	}
	if *list || strings.TrimSpace(*archiveID+*unarchiveID+*archiveSince+*archiveUntil) != "" {
		fmt.Fprintln(os.Stderr, "session menu action flags require --no-interactive")
		return 2
	}
	return runSessionMenuInteractive()
}

func runSessionMenuNonInteractive(list bool, archiveID, unarchiveID, archiveSince, archiveUntil string) int {
	actions := 0
	if list {
		actions++
	}
	if strings.TrimSpace(archiveID) != "" {
		actions++
	}
	if strings.TrimSpace(unarchiveID) != "" {
		actions++
	}
	if strings.TrimSpace(archiveSince) != "" {
		actions++
	}
	if actions != 1 || (strings.TrimSpace(archiveUntil) != "" && strings.TrimSpace(archiveSince) == "") {
		fmt.Fprintln(os.Stderr, "session menu --no-interactive requires exactly one action: --list, --archive, --unarchive, or --archive-since")
		return 2
	}
	if list {
		return handleSessionListCommand(nil)
	}
	if id := strings.TrimSpace(archiveID); id != "" {
		return confirmAndRunSessionMenuAction(true, fmt.Sprintf("Archive session %q.", id), func() int {
			return handleSessionArchiveCommand([]string{id})
		})
	}
	if id := strings.TrimSpace(unarchiveID); id != "" {
		return confirmAndRunSessionMenuAction(true, fmt.Sprintf("Unarchive session %q.", id), func() int {
			return handleSessionUnarchiveCommand([]string{id})
		})
	}
	forwarded := []string{"--since", strings.TrimSpace(archiveSince)}
	if until := strings.TrimSpace(archiveUntil); until != "" {
		forwarded = append(forwarded, "--until", until)
	}
	return confirmAndRunSessionMenuAction(true, "Archive sessions in the selected date range.", func() int {
		return handleSessionArchiveCommand(forwarded)
	})
}

func runSessionMenuInteractive() int {
	theme, err := presentation.Resolve(string(presentation.ProfileTerminal), os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Session menu theme error: %v\n", err)
		return 1
	}
	options := []initMenuOption{
		{label: "List sessions", value: "list"},
		{label: "Archive by ID", value: "archive-id"},
		{label: "Archive by date range", value: "archive-range"},
		{label: "Unarchive by ID", value: "unarchive-id"},
		{label: "Exit", value: "exit"},
	}
	for {
		choice, err := promptInitMenu(os.Stdin, os.Stdout, int(os.Stdin.Fd()), "Session management", "Use Up/Down arrows and Enter.", options, theme)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Session menu failed: %v\n", err)
			return 1
		}
		switch choice {
		case "list":
			if code := handleSessionListCommand(nil); code != 0 {
				return code
			}
		case "archive-id":
			id, ok := promptSessionMenuValue("Session ID", true)
			if !ok {
				return 1
			}
			if code := confirmAndRunSessionMenuAction(false, fmt.Sprintf("Archive session %q.", id), func() int {
				return handleSessionArchiveCommand([]string{id})
			}); code != 0 {
				return code
			}
		case "archive-range":
			since, ok := promptSessionMenuValue("Since (YYYY-MM-DD or RFC3339)", true)
			if !ok {
				return 1
			}
			until, ok := promptSessionMenuValue("Until (optional)", false)
			if !ok {
				return 1
			}
			forwarded := []string{"--since", since}
			if until != "" {
				forwarded = append(forwarded, "--until", until)
			}
			if code := confirmAndRunSessionMenuAction(false, "Archive sessions in the selected date range.", func() int {
				return handleSessionArchiveCommand(forwarded)
			}); code != 0 {
				return code
			}
		case "unarchive-id":
			id, ok := promptSessionMenuValue("Session ID", true)
			if !ok {
				return 1
			}
			if code := confirmAndRunSessionMenuAction(false, fmt.Sprintf("Unarchive session %q.", id), func() int {
				return handleSessionUnarchiveCommand([]string{id})
			}); code != 0 {
				return code
			}
		case "exit":
			return 0
		}
	}
}

func confirmAndRunSessionMenuAction(noInteractive bool, summary string, runAction func() int) int {
	confirmed, err := confirmConfigChange(noInteractive, summary)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Session menu confirmation failed: %v\n", err)
		return 1
	}
	if !confirmed {
		fmt.Fprintln(os.Stdout, "Session menu action cancelled.")
		return 0
	}
	return runAction()
}

func promptSessionMenuValue(label string, required bool) (string, bool) {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Fprintf(os.Stdout, "%s: ", label)
		value, err := readInitLine(reader)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Session menu input failed: %v\n", err)
			return "", false
		}
		value = strings.TrimSpace(value)
		if value != "" || !required {
			return value, true
		}
		fmt.Fprintf(os.Stdout, "%s is required.\n", label)
	}
}
