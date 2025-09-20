package main

import (
    "bufio"
    "encoding/json"
    "errors"
    "flag"
    "fmt"
    "io"
    "log"
    "os"
    "path/filepath"
    "time"

    "github.com/tursomari/machtiani/patcher/internal/check"
    "github.com/tursomari/machtiani/patcher/internal/diff"
    "github.com/tursomari/machtiani/patcher/internal/engine"
    fsutil "github.com/tursomari/machtiani/patcher/internal/fs"
    "github.com/tursomari/machtiani/patcher/internal/instructions"
)

// Exit codes
const (
    exitOK              = 0
    exitInvalidInput    = 2
    exitPatchGenFailed  = 3
    exitPatchNotClean   = 4
    exitInternalError   = 1
)

func main() { os.Exit(run()) }

func run() int {
    var (
        repo    string
        session string
        input   string
        outDir  string
        verbose bool
    )

    flag.StringVar(&repo, "repo", "", "Repository root where .git lives (required)")
    flag.StringVar(&session, "session", "", "Session ID (required)")
    flag.StringVar(&input, "input", "-", "Path to JSON instructions or '-' for stdin (required)")
    flag.StringVar(&outDir, "out-dir", "", "Output directory for patch (default: <repo>/.mct/patches/<session>)")
    flag.BoolVar(&verbose, "verbose", false, "Enable verbose logging to stderr")
    flag.Parse()

    logger := log.New(io.Discard, "", log.LstdFlags)
    if verbose {
        logger.SetOutput(os.Stderr)
    }

    if repo == "" || session == "" || input == "" {
        fmt.Fprintln(os.Stderr, "--repo, --session, and --input are required")
        return exitInvalidInput
    }

    repoAbs, err := filepath.Abs(repo)
    if err != nil {
        fmt.Fprintf(os.Stderr, "invalid --repo: %v\n", err)
        return exitInvalidInput
    }
    if ok, err := fsutil.IsRepoRoot(repoAbs); err != nil || !ok {
        if err != nil {
            fmt.Fprintf(os.Stderr, "failed to inspect repo: %v\n", err)
        } else {
            fmt.Fprintf(os.Stderr, "--repo must point to a git repo root (no .git found): %s\n", repoAbs)
        }
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

    var instr instructions.Instructions
    dec := json.NewDecoder(in)
    dec.DisallowUnknownFields()
    if err := dec.Decode(&instr); err != nil {
        fmt.Fprintf(os.Stderr, "invalid input JSON: %v\n", err)
        return exitInvalidInput
    }

    if err := instructions.Validate(repoAbs, instr); err != nil {
        if errors.Is(err, instructions.ErrInvalid) {
            fmt.Fprintf(os.Stderr, "invalid instructions: %v\n", err)
            return exitInvalidInput
        }
        fmt.Fprintf(os.Stderr, "validation error: %v\n", err)
        return exitInternalError
    }

    // Compute after-state for each file.
    logger.Println("Applying edits in memory...")
    afterMap, filesTouched, err := engine.ApplyAll(repoAbs, instr)
    if err != nil {
        if errors.Is(err, engine.ErrEditConflict) || errors.Is(err, engine.ErrEditFailed) {
            fmt.Fprintf(os.Stderr, "failed to apply edits: %v\n", err)
            return exitInvalidInput
        }
        fmt.Fprintf(os.Stderr, "engine error: %v\n", err)
        return exitInternalError
    }

    // Create temp mirror and generate patch via git diff --no-index.
    logger.Println("Generating patch via git diff --no-index...")
    mirrorDir, cleanup, err := fsutil.MakeTempMirror(repoAbs, afterMap)
    if err != nil {
        fmt.Fprintf(os.Stderr, "failed to create mirror: %v\n", err)
        return exitInternalError
    }
    defer cleanup()

    patchBytes, err := diff.Generate(repoAbs, mirrorDir, filesTouched)
    if err != nil {
        fmt.Fprintf(os.Stderr, "failed to generate patch: %v\n", err)
        return exitPatchGenFailed
    }

    // Ensure output dir.
    if outDir == "" {
        outDir = filepath.Join(repoAbs, ".mct", "patches", session)
    } else {
        // Allow relative outDir to repo.
        if !filepath.IsAbs(outDir) {
            outDir = filepath.Join(repoAbs, outDir)
        }
    }
    if err := os.MkdirAll(outDir, 0o755); err != nil {
        fmt.Fprintf(os.Stderr, "failed to create out-dir: %v\n", err)
        return exitInternalError
    }

    // Name file with timestamp and short rand.
    patchPath := filepath.Join(outDir, fsutil.PatchFilename(time.Now()))
    if err := os.WriteFile(patchPath, patchBytes, 0o644); err != nil {
        fmt.Fprintf(os.Stderr, "failed to write patch: %v\n", err)
        return exitInternalError
    }

    // Validate applicability.
    logger.Println("Validating patch with git apply --check...")
    if err := check.ApplyCheck(repoAbs, patchPath); err != nil {
        fmt.Fprintf(os.Stderr, "patch does not apply cleanly: %v\n", err)
        return exitPatchNotClean
    }

    // Extract stats and files list.
    stats := diff.ExtractStats(patchBytes)
    // Prefer the explicit touched files list for clearer relative paths.
    stats.FilesModified = filesTouched

    // Emit machine-readable JSON to stdout.
    out := map[string]any{
        "patch_path":    patchPath,
        "applies":       true,
        "files_modified": stats.FilesModified,
        "insertions":    stats.Insertions,
        "deletions":     stats.Deletions,
    }
    if instr.Metadata != nil && instr.Metadata.Description != "" {
        out["description"] = instr.Metadata.Description
    }
    enc := json.NewEncoder(os.Stdout)
    if err := enc.Encode(out); err != nil {
        fmt.Fprintf(os.Stderr, "failed to encode output: %v\n", err)
        return exitInternalError
    }
    return exitOK
}
