package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	mctpatcher "github.com/tursomari/machtiani/mct/patcher"
	patchersvc "github.com/tursomari/machtiani/patcher"
)

var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = "unknown"
	Dirty   = "unknown"
)

// Exit codes
const (
	exitOK             = 0
	exitInvalidInput   = 2
	exitPatchGenFailed = 3
	exitPatchNotClean  = 4
	exitInternalError  = 1
)

func main() { os.Exit(run()) }

func run() int {
	var (
		repo        string
		session     string
		input       string
		outDir      string
		verbose     bool
		showVersion bool
	)

	flag.StringVar(&repo, "repo", "", "Repository root where .git lives (required)")
	flag.StringVar(&session, "session", "", "Session ID (required)")
	flag.StringVar(&input, "input", "-", "Path to JSON instructions or '-' for stdin (required)")
	flag.StringVar(&outDir, "out-dir", "", "Output directory for patch (default: <repo>/.machtiani/artifacts/patches/<session>)")
	flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging to stderr")
	flag.BoolVar(&showVersion, "version", false, "Print version information and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("patcher %s\ncommit: %s\nbuilt: %s\ndirty: %s\n", Version, Commit, BuiltAt, Dirty)
		return exitOK
	}

	if repo == "" || session == "" || input == "" {
		fmt.Fprintln(os.Stderr, "--repo, --session, and --input are required")
		return exitInvalidInput
	}

	var in io.Reader
	if input == "-" {
		in = bufio.NewReader(os.Stdin)
	} else {
		f, err := os.Open(input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open input: %v\n", err)
			return exitInvalidInput
		}
		defer f.Close()
		in = f
	}

	var instr mctpatcher.Instructions
	dec := json.NewDecoder(in)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&instr); err != nil {
		fmt.Fprintf(os.Stderr, "invalid input JSON: %v\n", err)
		return exitInvalidInput
	}

	ctx := context.Background()
	logger := log.New(os.Stderr, "", log.LstdFlags)
	svc := patchersvc.NewService(patchersvc.WithLogger(logger))
	params := mctpatcher.PatchParams{
		RepoRoot:     repo,
		SessionID:    session,
		Instructions: instr,
		OutputDir:    outDir,
		Verbose:      verbose,
	}
	result, err := svc.ApplyAndGeneratePatch(ctx, params)
	if err != nil {
		var vErr *mctpatcher.ValidationError
		var genErr *mctpatcher.PatchGenerationError
		var cleanErr *mctpatcher.PatchNotCleanError
		switch {
		case errors.As(err, &vErr):
			fmt.Fprintln(os.Stderr, vErr.Error())
			return exitInvalidInput
		case errors.As(err, &genErr):
			fmt.Fprintln(os.Stderr, genErr.Error())
			return exitPatchGenFailed
		case errors.As(err, &cleanErr):
			fmt.Fprintln(os.Stderr, cleanErr.Error())
			return exitPatchNotClean
		default:
			fmt.Fprintf(os.Stderr, "patcher error: %v\n", err)
			return exitInternalError
		}
	}

	out := map[string]any{
		"patch_path":     result.PatchPath,
		"applies":        true,
		"files_modified": result.FilesModified,
		"insertions":     result.Insertions,
		"deletions":      result.Deletions,
	}
	if strings.TrimSpace(result.Description) != "" {
		out["description"] = result.Description
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "failed to encode output: %v\n", err)
		return exitInternalError
	}
	return exitOK
}
