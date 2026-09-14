package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

func TestMigrateDryRunDoesNotWrite(t *testing.T) {
	home, project := setupMigrationTest(t)
	writeMigrationFixture(t, project)

	stdout, stderr := captureOutput(func() {
		if code := handleMigrateCommand([]string{"--dry-run", "--json", "--no-interactive"}); code != 0 {
			t.Fatalf("migrate dry-run exit = %d", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	var report migrationReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	if !report.DryRun || report.ConfigScope != string(projectstore.ScopeProject) || len(report.Entries) != 3 {
		t.Fatalf("report = %#v", report)
	}
	if _, err := os.Stat(projectstore.MarkerPath(project)); !os.IsNotExist(err) {
		t.Fatalf("dry-run marker stat = %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".machtiani", report.UUID)); !os.IsNotExist(err) {
		t.Fatalf("dry-run store stat = %v", err)
	}
}

func TestMigrateRequiresExplicitNonInteractiveConfirmation(t *testing.T) {
	_, project := setupMigrationTest(t)
	writeMigrationFixture(t, project)

	_, stderr := captureOutput(func() {
		if code := handleMigrateCommand([]string{"--no-interactive"}); code != 2 {
			t.Fatalf("migrate exit = %d, want 2", code)
		}
	})
	if stderr == "" {
		t.Fatal("expected confirmation guidance")
	}
	if _, err := os.Stat(projectstore.MarkerPath(project)); !os.IsNotExist(err) {
		t.Fatalf("marker stat = %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".machtiani", "sessions", "s1", "answer.md")); err != nil {
		t.Fatalf("legacy fixture changed: %v", err)
	}
}

func TestMigrateCopiesVerifiesMarksAndArchives(t *testing.T) {
	home, project := setupMigrationTest(t)
	writeMigrationFixture(t, project)
	migrateNow = func() time.Time { return time.Date(2026, 7, 14, 18, 30, 0, 0, time.UTC) }
	t.Cleanup(func() { migrateNow = time.Now })

	stdout, stderr := captureOutput(func() {
		if code := handleMigrateCommand([]string{"--no-interactive", "--yes", "--json"}); code != 0 {
			t.Fatalf("migrate exit = %d", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	var report migrationReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	ctx, err := projectstore.Discover(project)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Status != projectstore.StatusInitialized || ctx.ID.String() != report.UUID || ctx.ConfigScope != projectstore.ScopeProject {
		t.Fatalf("context = %#v, report = %#v", ctx, report)
	}
	if ctx.StoreRoot != filepath.Join(home, ".machtiani", report.UUID) {
		t.Fatalf("store = %s", ctx.StoreRoot)
	}
	assertFileContents(t, filepath.Join(ctx.StoreRoot, "config.toml"), "default_model = \"fixture\"\n")
	assertFileContents(t, filepath.Join(ctx.SessionsRoot(), "s1", "answer.md"), "answer\n")
	assertFileContents(t, filepath.Join(ctx.ArtifactsRoot(), "readme", "internal-readme.md"), "background\n")
	archive := filepath.Join(project, ".machtiani.legacy-20260714T183000Z")
	assertFileContents(t, filepath.Join(archive, "sessions", "s1", "answer.md"), "answer\n")
	if _, err := os.Stat(filepath.Join(project, ".machtiani", "sessions")); !os.IsNotExist(err) {
		t.Fatalf("legacy sessions stat = %v", err)
	}
	if report.Files != 3 || report.Bytes == 0 {
		t.Fatalf("verification stats = %d files, %d bytes", report.Files, report.Bytes)
	}
}

func TestMigrateCanKeepVerifiedLegacyState(t *testing.T) {
	_, project := setupMigrationTest(t)
	writeMigrationFixture(t, project)

	if code := handleMigrateCommand([]string{"--no-interactive", "--yes", "--keep-legacy"}); code != 0 {
		t.Fatalf("migrate exit = %d", code)
	}
	assertFileContents(t, filepath.Join(project, ".machtiani", "sessions", "s1", "answer.md"), "answer\n")
}

func TestMigrateDryRunReportsDisposableSessionData(t *testing.T) {
	home, project := setupMigrationTest(t)
	writeMigrationFixture(t, project)
	writeDisposableMigrationFixture(t, project)

	stdout, stderr := captureOutput(func() {
		if code := handleMigrateCommand([]string{"--dry-run", "--json", "--no-interactive"}); code != 0 {
			t.Fatalf("migrate dry-run exit = %d", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	var report migrationReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, stdout)
	}
	if report.Files != 5 || report.SkippedLLMInputFiles != 1 || report.SkippedLLMInputBytes != int64(len("full input")) {
		t.Fatalf("dry-run report = %#v", report)
	}
	if report.SkippedShellAgentStateFiles != 2 || report.SkippedShellAgentStateBytes != int64(2*len("state")) {
		t.Fatalf("dry-run state stats = %#v", report)
	}
	if _, err := os.Stat(filepath.Join(home, ".machtiani", report.UUID)); !os.IsNotExist(err) {
		t.Fatalf("dry-run store stat = %v", err)
	}
}

func TestMigrateExcludesDisposableSessionDataWithoutEmptyDirectories(t *testing.T) {
	home, project := setupMigrationTest(t)
	writeMigrationFixture(t, project)
	writeDisposableMigrationFixture(t, project)
	migrateNow = func() time.Time { return time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { migrateNow = time.Now })

	stdout, stderr := captureOutput(func() {
		if code := handleMigrateCommand([]string{"--no-interactive", "--yes", "--json"}); code != 0 {
			t.Fatalf("migrate exit = %d", code)
		}
	})
	if stderr != "" {
		t.Fatalf("stderr = %q", stderr)
	}
	var report migrationReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(home, ".machtiani", report.UUID)
	for _, excluded := range []string{
		filepath.Join(store, "sessions", "s1", "artifacts", "llm", "inputs.jsonl"),
		filepath.Join(store, "sessions", "s1", "shell-agent", "1", "state.json"),
		filepath.Join(store, "sessions", "s1", "shell-agent", "2"),
	} {
		if _, err := os.Lstat(excluded); !os.IsNotExist(err) {
			t.Fatalf("excluded path exists %s: %v", excluded, err)
		}
	}
	assertFileContents(t, filepath.Join(store, "sessions", "s1", "trajectory", "agent.jsonl"), "agent\n")
	assertFileContents(t, filepath.Join(store, "sessions", "s1", "shell-agent", "1", "trajectory.json"), "trajectory\n")

	archive := filepath.Join(project, ".machtiani.legacy-20260714T200000Z")
	assertFileContents(t, filepath.Join(archive, "sessions", "s1", "artifacts", "llm", "inputs.jsonl"), "full input")
	assertFileContents(t, filepath.Join(archive, "sessions", "s1", "shell-agent", "2", "state.json"), "state")
}

func setupMigrationTest(t *testing.T) (home, project string) {
	t.Helper()
	home = t.TempDir()
	project = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MACHTIANI_CONFIG", "")
	cmd := exec.Command("git", "init", "--quiet", project)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	return home, project
}

func writeMigrationFixture(t *testing.T, project string) {
	t.Helper()
	for path, contents := range map[string]string{
		filepath.Join(project, ".machtiani", "config.toml"):                               "default_model = \"fixture\"\n",
		filepath.Join(project, ".machtiani", "sessions", "s1", "answer.md"):               "answer\n",
		filepath.Join(project, ".machtiani", "artifacts", "readme", "internal-readme.md"): "background\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func writeDisposableMigrationFixture(t *testing.T, project string) {
	t.Helper()
	for path, contents := range map[string]string{
		filepath.Join(project, ".machtiani", "sessions", "s1", "artifacts", "llm", "inputs.jsonl"):    "full input",
		filepath.Join(project, ".machtiani", "sessions", "s1", "trajectory", "agent.jsonl"):           "agent\n",
		filepath.Join(project, ".machtiani", "sessions", "s1", "shell-agent", "1", "state.json"):      "state",
		filepath.Join(project, ".machtiani", "sessions", "s1", "shell-agent", "1", "trajectory.json"): "trajectory\n",
		filepath.Join(project, ".machtiani", "sessions", "s1", "shell-agent", "2", "state.json"):      "state",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
