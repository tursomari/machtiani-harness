package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/pflag"
	"github.com/tursomari/machtiani/agent/internal/modes"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
	"golang.org/x/term"
)

var migrateNow = time.Now

type migrationEntry struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type migrationReport struct {
	DryRun      bool             `json:"dry_run"`
	UUID        string           `json:"uuid"`
	Source      string           `json:"source"`
	Store       string           `json:"store"`
	Archive     string           `json:"archive,omitempty"`
	ConfigScope string           `json:"config_scope"`
	Entries     []migrationEntry `json:"entries"`
	Files       int              `json:"files"`
	Bytes       int64            `json:"bytes"`
}

type migrationStats struct {
	files int
	bytes int64
}

func init() {
	cliCommands = append(cliCommands, cliCommand{
		name:        "migrate",
		description: "Migrate legacy project state into the UUID home store",
		handler:     handleMigrateCommand,
	})
}

func handleMigrateCommand(args []string) int {
	fs := pflag.NewFlagSet("mct-agent migrate", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "show the migration without changing files")
	yes := fs.Bool("yes", false, "confirm migration and legacy-state archival")
	noInteractive := fs.Bool("no-interactive", false, "never prompt; requires --yes unless --dry-run")
	keepLegacy := fs.Bool("keep-legacy", false, "leave verified legacy state in place instead of archiving it")
	jsonOutput := fs.Bool("json", false, "print the migration report as JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mct-agent migrate [--dry-run] [--yes] [--no-interactive] [--keep-legacy] [--json]")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Copy and verify legacy project state in a UUID-backed home store, then archive the copied source entries.")
		fmt.Fprintln(os.Stderr, "Use --dry-run to inspect the exact source and destination. Automation must pass --no-interactive --yes.")
	}
	if err := fs.Parse(args); err != nil {
		if err == pflag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		return configUsageError("mct-agent migrate takes flags, not positional arguments")
	}

	ctx, err := projectstore.Discover("")
	if err != nil {
		return configError(err)
	}
	if ctx.Status == projectstore.StatusInitialized {
		return configError(fmt.Errorf("project is already initialized with UUID %s", ctx.ID))
	}
	entries, scope, err := planMigrationEntries(ctx)
	if err != nil {
		return configError(err)
	}
	id := uuid.New()
	store := filepath.Join(ctx.HomeRoot, id.String())
	archive := ""
	if !*keepLegacy {
		archive = filepath.Join(ctx.ProjectRoot, fmt.Sprintf(".machtiani.legacy-%s", migrateNow().UTC().Format("20060102T150405Z")))
	}
	report := migrationReport{
		DryRun:      *dryRun,
		UUID:        id.String(),
		Source:      filepath.Join(ctx.ProjectRoot, projectstore.RootDirName),
		Store:       store,
		Archive:     archive,
		ConfigScope: string(scope),
		Entries:     entries,
	}
	if *dryRun {
		printMigrationReport(report, *jsonOutput)
		return 0
	}
	if !*yes {
		if *noInteractive || !term.IsTerminal(int(os.Stdin.Fd())) {
			return configUsageError("migration requires confirmation; pass --yes (and --no-interactive for automation) or use --dry-run")
		}
		confirmed, promptErr := promptYesNo(bufio.NewReader(os.Stdin), os.Stdout, fmt.Sprintf("Migrate legacy state to %s? [y/N]: ", store), false)
		if promptErr != nil {
			return configError(promptErr)
		}
		if !confirmed {
			fmt.Fprintln(os.Stdout, "Migration cancelled.")
			return 0
		}
	}

	stats, err := executeMigration(ctx, id, entries, scope, archive)
	if err != nil {
		return configError(err)
	}
	report.Files = stats.files
	report.Bytes = stats.bytes
	if err := modes.SyncCanonical(); err != nil {
		return configError(fmt.Errorf("migration completed but canonical mode sync failed: %w", err))
	}
	printMigrationReport(report, *jsonOutput)
	return 0
}

func planMigrationEntries(ctx projectstore.Context) ([]migrationEntry, projectstore.ConfigScope, error) {
	legacyRoot := filepath.Join(ctx.ProjectRoot, projectstore.RootDirName)
	type item struct{ source, destination string }
	items := []item{
		{projectstore.GlobalConfig, projectstore.GlobalConfig},
		{projectstore.SessionsDirName, projectstore.SessionsDirName},
		{projectstore.ArtifactsDirName, projectstore.ArtifactsDirName},
		{projectstore.ScratchDirName, projectstore.ScratchDirName},
		{projectstore.MetaDirName, projectstore.MetaDirName},
	}
	entries := make([]migrationEntry, 0, len(items))
	scope := projectstore.ScopeGlobal
	for _, candidate := range items {
		source := filepath.Join(legacyRoot, candidate.source)
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, "", fmt.Errorf("inspect legacy state %s: %w", source, err)
		}
		if candidate.source == projectstore.GlobalConfig {
			scope = projectstore.ScopeProject
		}
		entries = append(entries, migrationEntry{Name: candidate.source, Source: source, Destination: candidate.destination})
	}
	if len(entries) == 0 {
		return nil, "", fmt.Errorf("no migratable legacy state found under %s", legacyRoot)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries, scope, nil
}

func executeMigration(ctx projectstore.Context, id uuid.UUID, entries []migrationEntry, scope projectstore.ConfigScope, archive string) (migrationStats, error) {
	if err := os.MkdirAll(ctx.HomeRoot, 0o700); err != nil {
		return migrationStats{}, fmt.Errorf("create home store root: %w", err)
	}
	store := filepath.Join(ctx.HomeRoot, id.String())
	if _, err := os.Stat(store); err == nil {
		return migrationStats{}, fmt.Errorf("destination already exists: %s", store)
	} else if !errors.Is(err, os.ErrNotExist) {
		return migrationStats{}, fmt.Errorf("inspect destination %s: %w", store, err)
	}
	staging, err := os.MkdirTemp(ctx.HomeRoot, ".migrate-"+id.String()+"-")
	if err != nil {
		return migrationStats{}, fmt.Errorf("create migration staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o700); err != nil {
		return migrationStats{}, err
	}
	if err := projectstore.EnsureLayout(staging); err != nil {
		return migrationStats{}, err
	}

	stats := migrationStats{}
	for _, entry := range entries {
		destination := filepath.Join(staging, entry.Destination)
		if err := copyAndVerify(entry.Source, destination, &stats); err != nil {
			return migrationStats{}, fmt.Errorf("copy %s: %w", entry.Name, err)
		}
	}
	if err := projectstore.WriteConfigScope(staging, scope); err != nil {
		return migrationStats{}, err
	}
	if err := os.Rename(staging, store); err != nil {
		return migrationStats{}, fmt.Errorf("publish migrated store %s: %w", store, err)
	}
	if err := projectstore.WriteProjectUUID(ctx.ProjectRoot, id); err != nil {
		return migrationStats{}, fmt.Errorf("write project marker after verified copy: %w", err)
	}
	if archive != "" {
		if err := archiveMigrationEntries(entries, archive); err != nil {
			return migrationStats{}, fmt.Errorf("home store and marker are complete, but legacy archival failed: %w", err)
		}
	}
	return stats, nil
}

func copyAndVerify(source, destination string, stats *migrationStats) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	switch {
	case info.Mode().IsDir():
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
		children, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := copyAndVerify(filepath.Join(source, child.Name()), filepath.Join(destination, child.Name()), stats); err != nil {
				return err
			}
		}
		return nil
	case info.Mode().IsRegular():
		if err := copyRegularFile(source, destination, info.Mode()); err != nil {
			return err
		}
		equal, err := filesEqual(source, destination)
		if err != nil {
			return err
		}
		if !equal {
			return errors.New("verification checksum mismatch")
		}
		stats.files++
		stats.bytes += info.Size()
		return nil
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return err
		}
		if err := os.Symlink(target, destination); err != nil {
			return err
		}
		copiedTarget, err := os.Readlink(destination)
		if err != nil || copiedTarget != target {
			return errors.New("verification symlink mismatch")
		}
		stats.files++
		return nil
	default:
		return fmt.Errorf("unsupported file type: %s", source)
	}
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	permissions := mode.Perm() & 0o700
	if permissions == 0 {
		permissions = 0o600
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, permissions)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func filesEqual(left, right string) (bool, error) {
	l, err := os.Open(left)
	if err != nil {
		return false, err
	}
	defer l.Close()
	r, err := os.Open(right)
	if err != nil {
		return false, err
	}
	defer r.Close()
	lHash := sha256.New()
	rHash := sha256.New()
	if _, err := io.Copy(lHash, l); err != nil {
		return false, err
	}
	if _, err := io.Copy(rHash, r); err != nil {
		return false, err
	}
	return string(lHash.Sum(nil)) == string(rHash.Sum(nil)), nil
}

func archiveMigrationEntries(entries []migrationEntry, archive string) error {
	if _, err := os.Stat(archive); err == nil {
		return fmt.Errorf("archive already exists: %s", archive)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(archive, 0o700); err != nil {
		return err
	}
	moved := make([]migrationEntry, 0, len(entries))
	for _, entry := range entries {
		target := filepath.Join(archive, entry.Name)
		if err := os.Rename(entry.Source, target); err != nil {
			for i := len(moved) - 1; i >= 0; i-- {
				_ = os.Rename(filepath.Join(archive, moved[i].Name), moved[i].Source)
			}
			_ = os.Remove(archive)
			return err
		}
		moved = append(moved, entry)
	}
	return nil
}

func printMigrationReport(report migrationReport, jsonOutput bool) {
	if jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(os.Stdout, string(data))
		return
	}
	action := "Migration complete"
	if report.DryRun {
		action = "Migration dry run"
	}
	fmt.Fprintf(os.Stdout, "%s\nUUID: %s\nSource: %s\nStore: %s\nConfig scope: %s\n", action, report.UUID, report.Source, report.Store, report.ConfigScope)
	if report.Archive != "" {
		fmt.Fprintf(os.Stdout, "Legacy archive: %s\n", report.Archive)
	} else {
		fmt.Fprintln(os.Stdout, "Legacy state: retained")
	}
	if !report.DryRun {
		fmt.Fprintf(os.Stdout, "Verified: %d files, %d bytes\n", report.Files, report.Bytes)
	}
	for _, entry := range report.Entries {
		fmt.Fprintf(os.Stdout, "- %s -> %s\n", entry.Source, filepath.Join(report.Store, entry.Destination))
	}
}
