package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/session"
)

func handleSessionArchiveCommand(args []string) int {
	return handleSessionArchiveChangeCommand(args, false)
}

func handleSessionUnarchiveCommand(args []string) int {
	return handleSessionArchiveChangeCommand(args, true)
}

func handleSessionArchiveChangeCommand(args []string, unarchive bool) int {
	action := "archive"
	pastAction := "Archived"
	if unarchive {
		action = "unarchive"
		pastAction = "Unarchived"
	}

	fs := pflag.NewFlagSet("machtiani session "+action, pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	sinceValue := fs.String("since", "", "include sessions updated on or after this date or RFC3339 timestamp")
	untilValue := fs.String("until", "", "include sessions updated on or before this date or RFC3339 timestamp")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: machtiani session %s <session-id>\n", action)
		fmt.Fprintf(os.Stderr, "       machtiani session %s --since <date> [--until <date>]\n\n", action)
		fmt.Fprintln(os.Stderr, "Dates may use YYYY-MM-DD or RFC3339 format.")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return 0
		}
		return 2
	}

	hasRange := strings.TrimSpace(*sinceValue) != "" || strings.TrimSpace(*untilValue) != ""
	if fs.NArg() == 1 && !hasRange {
		query := fs.Arg(0)
		id, err := session.ResolveSessionID(query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error resolving session %s: %v\n", query, err)
			return 1
		}
		var changeErr error
		if unarchive {
			changeErr = session.UnarchiveSession(id)
		} else {
			changeErr = session.ArchiveSession(id)
		}
		if changeErr != nil {
			fmt.Fprintf(os.Stderr, "Session %s failed: %v\n", action, changeErr)
			return 1
		}
		fmt.Fprintf(os.Stdout, "%s session %s.\n", pastAction, id)
		return 0
	}

	if fs.NArg() != 0 || strings.TrimSpace(*sinceValue) == "" {
		fmt.Fprintf(os.Stderr, "session %s requires either a session ID or --since <date>\n", action)
		fs.Usage()
		return 2
	}
	since, err := parseSessionArchiveDate(*sinceValue, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --since value: %v\n", err)
		return 2
	}
	options := session.ArchiveOptions{Since: &since}
	if strings.TrimSpace(*untilValue) != "" {
		until, err := parseSessionArchiveDate(*untilValue, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid --until value: %v\n", err)
			return 2
		}
		options.Until = &until
	}

	if unarchive {
		report, err := session.UnarchiveSessions(options)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Session unarchive failed: %v\n", err)
			return 1
		}
		printSessionArchiveReport("Unarchived", report.Unarchived, report.Skipped, report.IDs)
		return 0
	}
	report, err := session.ArchiveSessions(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Session archive failed: %v\n", err)
		return 1
	}
	printSessionArchiveReport("Archived", report.Archived, report.Skipped, report.IDs)
	return 0
}

func parseSessionArchiveDate(value string, endOfDay bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", value, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339, got %q", value)
	}
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return parsed, nil
}

func printSessionArchiveReport(action string, changed, skipped int, ids []string) {
	noun := "sessions"
	if changed == 1 {
		noun = "session"
	}
	fmt.Fprintf(os.Stdout, "%s %d %s; skipped %d.\n", action, changed, noun, skipped)
	if len(ids) > 0 {
		fmt.Fprintf(os.Stdout, "Sessions: %s\n", strings.Join(ids, ", "))
	}
}
