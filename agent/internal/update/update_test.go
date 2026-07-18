package update

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReceiptRoundTripAndPermissions(t *testing.T) {
	home := t.TempDir()
	paths := PathsForHome(home)
	want := Receipt{
		SchemaVersion:    2,
		Remote:           "file:///tmp/remote.git",
		DefaultBranch:    "rolling",
		SourceDir:        filepath.Join(home, "source"),
		Profile:          filepath.Join(home, "profile"),
		Prefix:           filepath.Join(home, "prefix"),
		BinaryPath:       filepath.Join(home, "prefix", "bin", "mct-agent"),
		InstalledCommit:  strings.Repeat("a", 40),
		InstalledVersion: "dev-aaaaaaaaaaaa",
		InstalledAt:      time.Unix(123, 0).UTC(),
	}
	if err := SaveReceipt(paths.Receipt, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadReceipt(paths.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("receipt mismatch\n got: %#v\nwant: %#v", got, want)
	}
	info, err := os.Stat(paths.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadConfigDefaultsAndValidation(t *testing.T) {
	paths := PathsForHome(t.TempDir())
	cfg, err := LoadConfig(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policy != PolicyPrompt || cfg.Cooldown != 24*time.Hour || cfg.FailureRetry != time.Hour {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if err := os.MkdirAll(filepath.Dir(paths.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Config, []byte("policy = \"bogus\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(paths.Config); err == nil || !strings.Contains(err.Error(), "policy") {
		t.Fatalf("expected policy validation error, got %v", err)
	}
}

func TestSanitizeRemoteRemovesCredentials(t *testing.T) {
	got, err := SanitizeRemote("https://alice:secret@example.test/org/repo.git")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.test/org/repo.git" {
		t.Fatalf("SanitizeRemote() = %q", got)
	}
}

func TestResolveRemoteHEADUsesSymbolicDefaultBranch(t *testing.T) {
	remote, work := makeRemote(t, "rolling")
	want := testGitOutput(t, work, "rev-parse", "HEAD")
	branch, commit, err := ResolveRemoteHEAD(context.Background(), remote)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "rolling" || commit != want {
		t.Fatalf("got branch=%q commit=%q, want rolling %q", branch, commit, want)
	}
}

func TestCheckDetectsRewrittenDefaultBranch(t *testing.T) {
	remote, work := makeRemote(t, "rolling")
	old := testGitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "commit", "--allow-empty", "-m", "next")
	runGit(t, work, "push", "origin", "rolling")
	newCommit := testGitOutput(t, work, "rev-parse", "HEAD")

	home := t.TempDir()
	paths := PathsForHome(home)
	receipt := Receipt{SchemaVersion: 2, Remote: remote, DefaultBranch: "rolling", SourceDir: work, Profile: filepath.Join(home, "profile"), Prefix: filepath.Join(home, "prefix"), BinaryPath: filepath.Join(home, "prefix", "bin", "mct-agent"), InstalledCommit: old}
	if err := SaveReceipt(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}
	m := NewManager(Options{Home: home})
	result, err := m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusAvailable || result.CandidateCommit != newCommit {
		t.Fatalf("unexpected result: %#v", result)
	}

	// Rewrite the release stream. Any different default-branch tip remains
	// authoritative, even when it is not a descendant of the installed commit.
	runGit(t, work, "reset", "--hard", "HEAD~1")
	runGit(t, work, "commit", "--allow-empty", "-m", "rewritten")
	runGit(t, work, "push", "--force", "origin", "rolling")
	rewritten := testGitOutput(t, work, "rev-parse", "HEAD")
	result, err = m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusAvailable || result.CandidateCommit != rewritten {
		t.Fatalf("rewrite result: %#v", result)
	}
}

func TestFetchFullHistoryRepairsShallowCloneAfterRewrite(t *testing.T) {
	remote, work := makeRemote(t, "rolling")
	runGit(t, work, "commit", "--allow-empty", "-m", "old second")
	runGit(t, work, "commit", "--allow-empty", "-m", "old tip")
	runGit(t, work, "push", "origin", "rolling")

	managed := filepath.Join(t.TempDir(), "managed")
	runGit(t, "", "clone", "--quiet", "--depth=1", "--single-branch", "file://"+remote, managed)
	if got := testGitOutput(t, managed, "rev-parse", "--is-shallow-repository"); got != "true" {
		t.Fatalf("managed clone shallow = %q, want true", got)
	}

	runGit(t, work, "reset", "--hard", "HEAD~2")
	runGit(t, work, "commit", "--allow-empty", "-m", "rewritten parent")
	parent := testGitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "commit", "--allow-empty", "-m", "rewritten tip")
	candidate := testGitOutput(t, work, "rev-parse", "HEAD")
	runGit(t, work, "push", "--force", "origin", "rolling")

	refspec := "+refs/heads/rolling:refs/remotes/origin/rolling"
	if err := fetchFullHistory(context.Background(), managed, refspec); err != nil {
		t.Fatal(err)
	}
	if got := testGitOutput(t, managed, "rev-parse", "--is-shallow-repository"); got != "false" {
		t.Fatalf("managed clone shallow = %q, want false", got)
	}
	if got := testGitOutput(t, managed, "rev-parse", "refs/remotes/origin/rolling"); got != candidate {
		t.Fatalf("fetched candidate = %q, want %q", got, candidate)
	}
	if got := testGitOutput(t, managed, "rev-parse", candidate+"^"); got != parent {
		t.Fatalf("candidate parent = %q, want %q", got, parent)
	}
}

func TestJSONResultDoesNotExposeCredentialedRemote(t *testing.T) {
	result := Result{Status: StatusAvailable, Remote: "https://example.test/repo.git"}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatalf("credential leaked: %s", data)
	}
}

func makeRemote(t *testing.T, branch string) (remote, work string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	work = filepath.Join(root, "work")
	runGit(t, root, "init", "--bare", remote)
	runGit(t, root, "init", "--initial-branch="+branch, work)
	runGit(t, work, "config", "user.email", "tests@example.invalid")
	runGit(t, work, "config", "user.name", "update tests")
	runGit(t, work, "commit", "--allow-empty", "-m", "initial")
	runGit(t, work, "remote", "add", "origin", remote)
	runGit(t, work, "push", "-u", "origin", branch)
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	return remote, work
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	gitArgs := args
	if dir != "" {
		gitArgs = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", gitArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func testGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
