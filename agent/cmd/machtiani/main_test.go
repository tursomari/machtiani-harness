package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/core/readmesync"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/session"
)

func TestSyncCommandUsesHeadCommit(t *testing.T) {
	prepareTestConfig(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv("MACHTIANI_README_TEST_STUB", "basic")

	repoDir := initTestRepo(t)
	if err := os.MkdirAll(filepath.Join(repoDir, ".machtiani", "artifacts"), 0o755); err != nil {
		t.Fatalf("create legacy artifact root: %v", err)
	}
	origWD := mustChdir(t, repoDir)
	defer mustChdir(t, origWD)

	var exitCode int
	stdout := captureStdout(t, func() {
		exitCode = handleSyncCommand(nil)
	})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !strings.Contains(stdout, "Readme synced for commit ") {
		t.Fatalf("expected sync success output, got %q", stdout)
	}
	if strings.Contains(stdout, "tokens") || strings.Contains(stdout, "sync ") {
		t.Fatalf("redirected sync output should not include the terminal footer, got %q", stdout)
	}

	readmePath := filepath.Join(repoDir, ".machtiani", "artifacts", "readme", "internal-readme.md")
	if _, err := os.Stat(readmePath); err != nil {
		t.Fatalf("expected readme file at %s: %v", readmePath, err)
	}
}

func TestSyncCommandPreservesErrorOutputWhenFooterIsNotTerminal(t *testing.T) {
	originalRun := readmeSyncRunFn
	t.Cleanup(func() { readmeSyncRunFn = originalRun })
	readmeSyncRunFn = func(context.Context, readmesync.Options) error {
		return errors.New("synthetic sync failure")
	}

	prepareTestConfig(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_MODEL", "test-model")
	repoDir := initTestRepo(t)
	origWD := mustChdir(t, repoDir)
	defer mustChdir(t, origWD)

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleSyncCommand(nil)
	})
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Readme sync failed: synthetic sync failure") {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if strings.Contains(stderr, "tokens") {
		t.Fatalf("redirected error output should not include the terminal footer: %q", stderr)
	}
}

func TestSyncCommandTurnTimeoutGlobalAndCLIOverride(t *testing.T) {
	originalRun := readmeSyncRunFn
	t.Cleanup(func() { readmeSyncRunFn = originalRun })
	var captured []readmesync.Options
	readmeSyncRunFn = func(_ context.Context, opts readmesync.Options) error {
		captured = append(captured, opts)
		return nil
	}

	prepareTestConfig(t)
	configPath := os.Getenv("MACHTIANI_CONFIG")
	if err := os.WriteFile(configPath, []byte("[planner]\nturn_timeout = 17\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	llm.ResetConfigForTesting()
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_MODEL", "test-model")
	repoDir := initTestRepo(t)
	origWD := mustChdir(t, repoDir)
	defer mustChdir(t, origWD)

	captureStdout(t, func() {
		if code := handleSyncCommand(nil); code != 0 {
			t.Fatalf("global timeout sync exit = %d", code)
		}
		if code := handleSyncCommand([]string{"--turn-timeout", "0"}); code != 0 {
			t.Fatalf("CLI timeout sync exit = %d", code)
		}
	})
	if len(captured) != 2 {
		t.Fatalf("sync calls = %d, want 2", len(captured))
	}
	if captured[0].TurnTimeout != 17 {
		t.Fatalf("global TurnTimeout = %d, want 17", captured[0].TurnTimeout)
	}
	if captured[1].TurnTimeout != 0 {
		t.Fatalf("CLI TurnTimeout = %d, want 0", captured[1].TurnTimeout)
	}
}

func TestSyncCommandInvalidCommit(t *testing.T) {
	prepareTestConfig(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_MODEL", "test-model")
	repoDir := initTestRepo(t)
	origWD := mustChdir(t, repoDir)
	defer mustChdir(t, origWD)

	exitCode := handleSyncCommand([]string{"--commit", "deadbeef"})
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code for invalid commit")
	}
}

func TestSyncCommandRequiresGitRepository(t *testing.T) {
	prepareTestConfig(t)
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://example.com/v1")
	t.Setenv("OPENAI_MODEL", "test-model")
	t.Setenv("MACHTIANI_README_TEST_STUB", "basic")

	tempDir := t.TempDir()
	origWD := mustChdir(t, tempDir)
	defer mustChdir(t, origWD)

	exitCode := handleSyncCommand(nil)
	if exitCode == 0 {
		t.Fatalf("expected non-zero exit code outside git repo")
	}
}

func TestRunCommandSucceedsWhenReadmeTagPresent(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) {
		return "abcdef123456", nil
	}
	readmeCommitForProjectFn = func(string) (string, error) {
		return "deadbeef", nil
	}
	called := false
	sessionRunFn = func(context.Context, session.Options) session.Result {
		called = true
		return session.Result{ExitCode: 0}
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"-p", "Investigate bug"})
	})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if !called {
		t.Fatalf("expected sessionRunFn to be called when README tag present")
	}
	if trimmed := strings.TrimSpace(stderr); trimmed != "" {
		t.Fatalf("expected no stderr output, got: %q", trimmed)
	}
}

func TestRunCommandFailsWhenReadmeMissing(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) {
		return "abcdef123456", nil
	}
	readmeCommitForProjectFn = func(string) (string, error) {
		return "", errors.New("git rev-parse oid-abc123 failed: exit status 1: fatal: ambiguous argument 'oid-abc123': unknown revision")
	}
	sessionRunFn = func(context.Context, session.Options) session.Result {
		t.Fatalf("sessionRunFn should not be called when README is missing")
		return session.Result{ExitCode: 0}
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"--dry-run", "-p", "Document behavior"})
	})
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Error: mct is not synced at current git state abcdef1.") {
		t.Fatalf("expected sync-required message, got: %q", stderr)
	}
	if !strings.Contains(stderr, "Run \u001b[1mmachtiani sync\u001b[0m before proceeding.") {
		t.Fatalf("expected sync instruction, got: %q", stderr)
	}
}

func TestRunCommandRejectsWholeFileSemanticErrorBeforeReadmeCheck(t *testing.T) {
	origHead := readmeHeadCommitFn
	t.Cleanup(func() { readmeHeadCommitFn = origHead })

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `default_model = "active"

[providers.good]
base_url = "https://example.com/v1"
api_key = "test-key"

[models.active]
provider = "good"
model = "active-model"

[models.dormant]
provider = "missing"
model = "dormant-model"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", path)
	llm.ResetConfigForTesting()
	readmeHeadCommitFn = func() (string, error) {
		t.Fatal("README check must not run after config validation fails")
		return "", nil
	}

	var code int
	stderr := captureStderr(t, func() {
		code = handleRunCommand([]string{"-p", "test"})
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "models.dormant.provider") {
		t.Fatalf("expected dormant provider diagnostic, got %q", stderr)
	}
}

func TestSyncCommandRejectsWholeFileSemanticErrorBeforeGitCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	content := `default_model = "active"

[providers.good]
base_url = "https://example.com/v1"
api_key = "test-key"

[models.active]
provider = "good"
model = "active-model"

[models.dormant]
provider = "missing"
model = "dormant-model"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", path)
	llm.ResetConfigForTesting()
	origWD := mustChdir(t, dir)
	defer mustChdir(t, origWD)

	var code int
	stderr := captureStderr(t, func() {
		code = handleSyncCommand(nil)
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "models.dormant.provider") {
		t.Fatalf("expected config diagnostic before git diagnostic, got %q", stderr)
	}
	if strings.Contains(stderr, "not a git repository") {
		t.Fatalf("git check ran before config validation: %q", stderr)
	}
}

func TestRunAndSyncHelpWorkWithInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("unknown_key = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACHTIANI_CONFIG", path)
	llm.ResetConfigForTesting()

	if code := handleRunCommand([]string{"--help"}); code != 0 {
		t.Fatalf("run --help returned %d", code)
	}
	llm.ResetConfigForTesting()
	if code := handleSyncCommand([]string{"--help"}); code != 0 {
		t.Fatalf("sync --help returned %d", code)
	}
}

func TestRunCommandFailsWhenRepoHasNoCommits(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) {
		return "", errors.New("git repository exists but has no commits yet")
	}
	readmeCommitForProjectFn = func(string) (string, error) {
		t.Fatalf("README lookup should not be attempted when HEAD is unavailable")
		return "", nil
	}
	sessionRunFn = func(context.Context, session.Options) session.Result {
		t.Fatalf("sessionRunFn should not be called when HEAD is unavailable")
		return session.Result{}
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"-p", "Assess repo"})
	})
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Git repository has no commits yet") {
		t.Fatalf("expected no commits message, got: %q", stderr)
	}
}

func TestRunCommandPropagatesShellAgentFlag(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) { return "abcdef123456", nil }
	readmeCommitForProjectFn = func(string) (string, error) { return "deadbeef", nil }

	var received session.Options
	sessionRunFn = func(ctx context.Context, opts session.Options) session.Result {
		received = opts
		return session.Result{ExitCode: 0}
	}

	if exit := handleRunCommand([]string{"--shell-agent", "-p", "Investigate env drift"}); exit != 0 {
		t.Fatalf("expected exit code 0, got %d", exit)
	}

	if !received.Config.ShellAgent {
		t.Fatalf("expected ShellAgent flag to propagate to session config")
	}
	if received.Goal != "Investigate env drift" {
		t.Fatalf("unexpected goal: %q", received.Goal)
	}
}

func TestRunCommandPropagatesShellAgentModel(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) { return "abcdef123456", nil }
	readmeCommitForProjectFn = func(string) (string, error) { return "deadbeef", nil }

	var received session.Options
	sessionRunFn = func(ctx context.Context, opts session.Options) session.Result {
		received = opts
		return session.Result{ExitCode: 0}
	}

	args := []string{"--shell-agent", "--shell-agent-model", "gpt-shell", "-p", "Diagnose drift"}
	if exit := handleRunCommand(args); exit != 0 {
		t.Fatalf("expected exit code 0, got %d", exit)
	}

	if received.Config.ShellAgentModel != "gpt-shell" {
		t.Fatalf("expected shell agent model override, got %q", received.Config.ShellAgentModel)
	}
	if received.Goal != "Diagnose drift" {
		t.Fatalf("unexpected goal: %q", received.Goal)
	}
}

func TestRunCommandDefaultsToFileDiscoveryMode(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) { return "abcdef123456", nil }
	readmeCommitForProjectFn = func(string) (string, error) { return "deadbeef", nil }

	var received session.Options
	sessionRunFn = func(ctx context.Context, opts session.Options) session.Result {
		received = opts
		return session.Result{ExitCode: 0}
	}

	if exit := handleRunCommand([]string{"-p", "Audit service rollout"}); exit != 0 {
		t.Fatalf("expected exit code 0, got %d", exit)
	}

	if received.Config.ShellAgent {
		t.Fatalf("expected shell agent to be disabled by default")
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.name", "Test User")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "initial commit")
	return repoDir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
}

func mustChdir(t *testing.T, dir string) string {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	return orig
}

func captureStderr(t *testing.T, fn func()) (captured string) {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	var builder strings.Builder
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&builder, r)
		r.Close()
		close(done)
	}()
	os.Stderr = w
	defer func() {
		_ = w.Close()
		<-done
		os.Stderr = orig
		captured = builder.String()
	}()
	fn()
	return
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	var builder strings.Builder
	done := make(chan struct{})
	go func() {
		io.Copy(&builder, r)
		r.Close()
		close(done)
	}()

	fn()
	w.Close()
	<-done
	return builder.String()
}

func TestConfigCheckOK(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := `
default_model = "gpt4"

[models]
[models.gpt4]
provider = "openai"
model = "gpt-4"

[providers.openai]
base_url = "https://example.com/v1"
api_key = "test-key"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", cfgPath)
	llm.ResetConfigForTesting()

	var code int
	stdout := captureStdout(t, func() {
		code = handleConfigCheckCommand([]string{})
	})
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "Config OK:") {
		t.Fatalf("expected 'Config OK:' in stdout, got %q", stdout)
	}
	if !strings.Contains(stdout, "Default model: gpt4") {
		t.Fatalf("expected 'Default model: gpt4' in stdout, got %q", stdout)
	}
}

func TestConfigCheckLoadError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("invalid toml [[[["), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", cfgPath)
	llm.ResetConfigForTesting()

	var code int
	stderr := captureStderr(t, func() {
		code = handleConfigCheckCommand([]string{})
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "Config error:") {
		t.Fatalf("expected 'Config error:' in stderr, got %q", stderr)
	}
}

func TestConfigCheckMissingDefaultModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", cfgPath)
	llm.ResetConfigForTesting()

	var code int
	stderr := captureStderr(t, func() {
		code = handleConfigCheckCommand([]string{})
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "default_model is required") {
		t.Fatalf("expected default_model error in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "configuration validation failed") {
		t.Fatalf("expected validation failure in stderr, got %q", stderr)
	}
}

func TestConfigCheckInvalidAlias(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := `
default_model = "bogus"

[models]
[models.gpt4]
provider = "openai"
model = "gpt-4"

[providers.openai]
base_url = "https://example.com/v1"
api_key = "test-key"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", cfgPath)
	llm.ResetConfigForTesting()

	var code int
	stderr := captureStderr(t, func() {
		code = handleConfigCheckCommand([]string{})
	})
	if code != 1 {
		t.Fatalf("expected exit 1, got %d", code)
	}
	if !strings.Contains(stderr, "model alias \"bogus\" is not defined") {
		t.Fatalf("expected alias error in stderr, got %q", stderr)
	}
}

func TestConfigCommandNoSubcommand(t *testing.T) {
	var code int
	stderr := captureStderr(t, func() {
		code = handleConfigCommand([]string{})
	})
	if code != 1 {
		t.Fatalf("expected exit 1 without a terminal, got %d", code)
	}
	if !strings.Contains(stderr, "interactive configuration requires a terminal") {
		t.Fatalf("expected terminal error in stderr, got %q", stderr)
	}
}

func TestConfigCommandUnknownSubcommand(t *testing.T) {
	var code int
	stderr := captureStderr(t, func() {
		code = handleConfigCommand([]string{"unknown"})
	})
	if code != 2 {
		t.Fatalf("expected exit 2, got %d", code)
	}
	if !strings.Contains(stderr, "Unknown config subcommand: unknown") {
		t.Fatalf("expected unknown subcommand error in stderr, got %q", stderr)
	}
	if !strings.Contains(stderr, "Usage: machtiani config") {
		t.Fatalf("expected usage in stderr, got %q", stderr)
	}
}

func TestPrintUsageIncludesConfigCheck(t *testing.T) {
	stderr := captureStderr(t, func() {
		printUsage()
	})
	if !strings.Contains(stderr, "config") {
		t.Fatalf("expected 'config' in usage output, got %q", stderr)
	}
}

func TestPrintUsageIncludesTopLevelFlags(t *testing.T) {
	stderr := captureStderr(t, func() {
		printUsage()
	})
	if !strings.Contains(stderr, "--version") {
		t.Fatalf("expected '--version' in usage output, got %q", stderr)
	}
	if !strings.Contains(stderr, "--help") {
		t.Fatalf("expected '--help' in usage output, got %q", stderr)
	}
	if !strings.Contains(stderr, "Flags:") {
		t.Fatalf("expected 'Flags:' section in usage output, got %q", stderr)
	}
}

func TestPrintUsageIncludesAllSubcommands(t *testing.T) {
	stderr := captureStderr(t, func() {
		printUsage()
	})
	for _, name := range []string{"run", "sync", "session", "config"} {
		if !strings.Contains(stderr, name) {
			t.Fatalf("expected %q in usage output, got %q", name, stderr)
		}
	}
}

func TestTopLevelFlagSetHelpExitsZero(t *testing.T) {
	fs := newTopLevelFlagSet()
	if err := fs.Parse([]string{"--help"}); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if h, _ := fs.GetBool("help"); !h {
		t.Fatalf("expected help flag to be true")
	}
}

func TestTopLevelFlagSetVersionFlag(t *testing.T) {
	fs := newTopLevelFlagSet()
	if err := fs.Parse([]string{"--version"}); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if v, _ := fs.GetBool("version"); !v {
		t.Fatalf("expected version flag to be true")
	}
}

func prepareTestConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("MACHTIANI_CONFIG", path)
	llm.ResetConfigForTesting()
}

func TestRunSessionFlagAliasesResolveToSessionID(t *testing.T) {
	for _, flag := range []string{"session-id", "resume"} {
		t.Run(flag, func(t *testing.T) {
			cfg := session.Config{}
			r := newRunFlagSet(&cfg)
			var output strings.Builder
			r.fs.SetOutput(&output)
			if err := r.fs.Parse([]string{"--" + flag, "agent-alias"}); err != nil {
				t.Fatalf("parse --%s: %v", flag, err)
			}
			if cfg.SessionID != "agent-alias" {
				t.Fatalf("--%s set SessionID = %q, want agent-alias", flag, cfg.SessionID)
			}
		})
	}
}

func TestSessionIDFlagEmitsDeprecationWarning(t *testing.T) {
	cfg := session.Config{}
	r := newRunFlagSet(&cfg)
	var output strings.Builder
	r.fs.SetOutput(&output)
	if err := r.fs.Parse([]string{"--session-id", "agent-legacy"}); err != nil {
		t.Fatalf("parse --session-id: %v", err)
	}
	if !strings.Contains(output.String(), "Flag --session-id has been deprecated") ||
		!strings.Contains(output.String(), "use 'machtiani run --resume <session-id>' or -r") {
		t.Fatalf("deprecation warning missing migration guidance: %q", output.String())
	}
	flag := r.fs.Lookup("session-id")
	if flag == nil || flag.Deprecated == "" || !flag.Hidden {
		t.Fatalf("--session-id was not marked deprecated: %#v", flag)
	}
}

func TestRunCommandRejectsConflictingSessionFlags(t *testing.T) {
	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"--session-id", "agent-one", "--resume", "agent-two", "-p", "Follow up"})
	})
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	for _, want := range []string{"session flags are mutually exclusive", "--session-id", "--resume"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("conflict error missing %q: %q", want, stderr)
		}
	}
}

// --- Tests for --file/-f flag on machtiani run ---

func TestFileFlagRegisteredInHelp(t *testing.T) {
	cfg := session.Config{}
	r := newRunFlagSet(&cfg)
	fs := r.fs

	f := fs.Lookup("file")
	if f == nil {
		t.Fatal("expected --file flag to be registered, got nil")
	}
	if f.Shorthand != "f" {
		t.Fatalf("expected --file shorthand 'f', got %q", f.Shorthand)
	}
	if f.DefValue != "" {
		t.Fatalf("expected --file default '', got %q", f.DefValue)
	}

	var buf strings.Builder
	fs.SetOutput(&buf)
	fs.Usage()
	if !strings.Contains(buf.String(), "--file") {
		t.Fatalf("expected --file in usage output, got: %q", buf.String())
	}
}

func TestLogLLMInputsFlagIsExplicitPerRun(t *testing.T) {
	cfg := session.Config{}
	r := newRunFlagSet(&cfg)
	if cfg.LogLLMInputs {
		t.Fatal("full LLM input logging must default to disabled")
	}
	if err := r.fs.Parse([]string{"--log-llm-inputs"}); err != nil {
		t.Fatal(err)
	}
	if !cfg.LogLLMInputs {
		t.Fatal("--log-llm-inputs did not enable session input logging")
	}
	help := r.fs.Lookup("log-llm-inputs")
	if help == nil || !strings.Contains(help.Usage, "source material") || !strings.Contains(help.Usage, "grow quickly") {
		t.Fatalf("warning-oriented flag help missing: %#v", help)
	}
}

func TestMarkExplicitModelOverrides(t *testing.T) {
	cfg := session.Config{}
	r := newRunFlagSet(&cfg)
	if err := r.fs.Parse([]string{
		"--model", "override-orchestrator",
		"--answer-model", "override-answer",
		"--file-discovery-model", "override-discovery",
		"--shell-agent-model", "override-shell",
	}); err != nil {
		t.Fatalf("parse model overrides: %v", err)
	}

	markExplicitModelOverrides(r.fs, &cfg)

	got := cfg.ModelOverrides
	if !got.Orchestrator || !got.Answer || !got.FileDiscovery || !got.ShellAgent || got.Direct {
		t.Fatalf("unexpected explicit model overrides: %#v", got)
	}
}

func TestMarkExplicitDirectModelOverride(t *testing.T) {
	cfg := session.Config{}
	r := newRunFlagSet(&cfg)
	if err := r.fs.Parse([]string{"--openai-model", "override-direct"}); err != nil {
		t.Fatalf("parse direct model override: %v", err)
	}

	markExplicitModelOverrides(r.fs, &cfg)

	if !cfg.ModelOverrides.Direct || cfg.ModelOverrides.Orchestrator {
		t.Fatalf("unexpected explicit direct model override: %#v", cfg.ModelOverrides)
	}
}

func TestFileFlagReadsGoalFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	goalPath := filepath.Join(tmpDir, "goal.txt")
	content := "goal from file\n"
	if err := os.WriteFile(goalPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)
	readmeHeadCommitFn = func() (string, error) { return "abcdef123456", nil }
	readmeCommitForProjectFn = func(string) (string, error) { return "deadbeef", nil }

	var received session.Options
	sessionRunFn = func(_ context.Context, opts session.Options) session.Result {
		received = opts
		return session.Result{ExitCode: 0}
	}

	exitCode := handleRunCommand([]string{"--file", goalPath})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if received.Goal != "goal from file" {
		t.Fatalf("expected goal %q, got %q", "goal from file", received.Goal)
	}
}

func TestFileFlagShorthandReadsGoalFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	goalPath := filepath.Join(tmpDir, "goal.txt")
	content := "goal via shorthand\n"
	if err := os.WriteFile(goalPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)
	readmeHeadCommitFn = func() (string, error) { return "abcdef123456", nil }
	readmeCommitForProjectFn = func(string) (string, error) { return "deadbeef", nil }

	var received session.Options
	sessionRunFn = func(_ context.Context, opts session.Options) session.Result {
		received = opts
		return session.Result{ExitCode: 0}
	}

	exitCode := handleRunCommand([]string{"-f", goalPath})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if received.Goal != "goal via shorthand" {
		t.Fatalf("expected goal %q, got %q", "goal via shorthand", received.Goal)
	}
}

func TestTextAndFileMutuallyExclusive(t *testing.T) {
	tmpDir := t.TempDir()
	goalPath := filepath.Join(tmpDir, "goal.txt")
	if err := os.WriteFile(goalPath, []byte("from file\n"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"--prompt", "inline goal", "--file", goalPath})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("expected 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

func TestNeitherTextNorFileProvided(t *testing.T) {
	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "one of --prompt or --file is required") {
		t.Fatalf("expected 'one of --prompt or --file is required' in stderr, got: %q", stderr)
	}
}

func TestFileFlagNonexistentFile(t *testing.T) {
	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"--file", "/no/such/path/goal.txt"})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Error reading file") {
		t.Fatalf("expected 'Error reading file' in stderr, got: %q", stderr)
	}
}

func TestUsageLineReflectsBothInputMethods(t *testing.T) {
	cfg := session.Config{}
	r := newRunFlagSet(&cfg)
	fs := r.fs

	var buf strings.Builder
	fs.SetOutput(&buf)
	fs.Usage()
	output := buf.String()
	if !strings.Contains(output, "--file") {
		t.Fatalf("expected --file in usage output, got: %q", output)
	}
	if !strings.Contains(output, "-p") {
		t.Fatalf("expected -p in usage output, got: %q", output)
	}
}

func TestPositionalArgumentsError(t *testing.T) {
	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"--prompt", "goal", "extra-arg"})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Use -p or --file to specify the prompt") {
		t.Fatalf("expected 'Use -p or --file to specify the prompt' in stderr, got: %q", stderr)
	}
}

func TestEmptyGoalError(t *testing.T) {
	tmpDir := t.TempDir()
	goalPath := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(goalPath, []byte(""), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"--file", goalPath})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "Provide non-empty content via -p or --file") {
		t.Fatalf("expected 'Provide non-empty content via -p or --file' in stderr, got: %q", stderr)
	}
}

// --- Tests for --tag flag behavior on machtiani run ---

func TestRunCommandTagFlagSetsBothTags(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) {
		return "abcdef123456", nil
	}
	readmeCommitForProjectFn = func(string) (string, error) {
		return "deadbeef", nil
	}

	var received session.Options
	sessionRunFn = func(ctx context.Context, opts session.Options) session.Result {
		received = opts
		return session.Result{ExitCode: 0}
	}

	exitCode := handleRunCommand([]string{"--tag", "foo", "-p", "task"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if received.Config.AnswerTag != "answer-foo" {
		t.Fatalf("expected AnswerTag 'answer-foo', got %q", received.Config.AnswerTag)
	}
	if received.Config.CommandTag != "command-foo" {
		t.Fatalf("expected CommandTag 'command-foo', got %q", received.Config.CommandTag)
	}
}

func TestRunCommandTagAndAnswerTagMutuallyExclusive(t *testing.T) {
	prepareTestConfig(t)

	var exitCode int
	stderr := captureStderr(t, func() {
		exitCode = handleRunCommand([]string{"--tag", "foo", "--answer-tag", "bar", "-p", "task"})
	})
	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Fatalf("expected 'mutually exclusive' in stderr, got: %q", stderr)
	}
}

func TestRunCommandDefaultCommandTag(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) {
		return "abcdef123456", nil
	}
	readmeCommitForProjectFn = func(string) (string, error) {
		return "deadbeef", nil
	}

	var received session.Options
	sessionRunFn = func(ctx context.Context, opts session.Options) session.Result {
		received = opts
		return session.Result{ExitCode: 0}
	}

	exitCode := handleRunCommand([]string{"-p", "task"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if received.Config.CommandTag != "command" {
		t.Fatalf("expected CommandTag 'command', got %q", received.Config.CommandTag)
	}
}

func TestRunCommandAnswerTagAlone(t *testing.T) {
	origHead := readmeHeadCommitFn
	origCommit := readmeCommitForProjectFn
	origSession := sessionRunFn
	t.Cleanup(func() {
		readmeHeadCommitFn = origHead
		readmeCommitForProjectFn = origCommit
		sessionRunFn = origSession
	})

	prepareTestConfig(t)

	readmeHeadCommitFn = func() (string, error) {
		return "abcdef123456", nil
	}
	readmeCommitForProjectFn = func(string) (string, error) {
		return "deadbeef", nil
	}

	var received session.Options
	sessionRunFn = func(ctx context.Context, opts session.Options) session.Result {
		received = opts
		return session.Result{ExitCode: 0}
	}

	exitCode := handleRunCommand([]string{"--answer-tag", "bar", "-p", "task"})
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", exitCode)
	}
	if received.Config.AnswerTag != "bar" {
		t.Fatalf("expected AnswerTag 'bar', got %q", received.Config.AnswerTag)
	}
	if received.Config.CommandTag != "command" {
		t.Fatalf("expected CommandTag 'command', got %q", received.Config.CommandTag)
	}
}
