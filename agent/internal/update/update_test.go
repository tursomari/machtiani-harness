package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNixGitInstallableIncludesVerifiedRefAndRevision(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source with space")
	got := nixGitInstallable(source, "release/next", strings.Repeat("a", 40))
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "git+file" || parsed.Path != filepath.ToSlash(source) || parsed.Fragment != "mct-agent" {
		t.Fatalf("unexpected installable identity: %q", got)
	}
	if parsed.Query().Get("ref") != "release/next" || parsed.Query().Get("rev") != strings.Repeat("a", 40) {
		t.Fatalf("installable omitted the verified ref or revision: %q", got)
	}
}

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

func TestCheckDetectsBinaryDivergence(t *testing.T) {
	remote, work := makeRemote(t, "rolling")
	remoteHEAD := testGitOutput(t, work, "rev-parse", "HEAD")

	home := t.TempDir()
	paths := PathsForHome(home)
	prefix := filepath.Join(home, "prefix")
	binaryPath := filepath.Join(prefix, "bin", "mct-agent")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// Fake binary reports a commit different from the receipt.
	staleCommit := strings.Repeat("d", 40)
	fakeScript := fmt.Sprintf("#!/bin/sh\necho 'mct-agent dev-%s'\necho 'commit: %s'\necho 'built: 20260808000000'\necho 'dirty: clean'\n", staleCommit[:12], staleCommit)
	if err := os.WriteFile(binaryPath, []byte(fakeScript), 0o755); err != nil {
		t.Fatal(err)
	}

	// Receipt says we are at remote HEAD, which differs from the actual binary.
	receipt := Receipt{
		SchemaVersion:    2,
		Remote:           "file://" + remote,
		DefaultBranch:    "rolling",
		SourceDir:        filepath.Join(home, "source"),
		Profile:          filepath.Join(home, "profile"),
		Prefix:           prefix,
		BinaryPath:       binaryPath,
		InstalledCommit:  remoteHEAD,
		InstalledVersion: "dev-" + remoteHEAD[:12],
		InstalledAt:      time.Now().UTC(),
	}
	if err := SaveReceipt(paths.Receipt, receipt); err != nil {
		t.Fatal(err)
	}

	m := NewManager(Options{Home: home})
	result, err := m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Remote HEAD matches receipt, so Check says "current" — but the
	// on-disk binary reports a different commit. The divergence must be
	// detected.
	if !result.InstalledDivergent {
		t.Fatalf("Check did not detect binary divergence: binary commit=%s, receipt commit=%s", staleCommit[:12], receipt.InstalledCommit[:12])
	}

	// Remove the binary entirely — Check must still detect divergence
	// because the installed binary is missing.
	if err := os.Remove(binaryPath); err != nil {
		t.Fatal(err)
	}
	result, err = m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.InstalledDivergent {
		t.Fatalf("Check did not detect divergence when binary is missing at %s", binaryPath)
	}
}

func TestUpdateReassertsBinarySymlink(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not available; skipping e2e update test")
	}
	projectRoot, err := findProjectRoot()
	if err != nil {
		t.Skip("could not locate project source: " + err.Error())
	}

	// Build the test infrastructure inside a single temp tree.
	root := t.TempDir()
	home := filepath.Join(root, "home")
	remoteDir := filepath.Join(root, "remote.git")
	seedDir := filepath.Join(root, "seed")
	prefix := filepath.Join(home, "prefix")
	binaryPath := filepath.Join(prefix, "bin", "mct-agent")

	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}

	// Seed: copy the project source and make it a fresh git repo.
	copyDir(t, projectRoot, seedDir,
		".git", ".gocache", ".machtiani", ".bench", ".data",
		"result", "third_party/skyvern",
	)
	runGit(t, seedDir, "init", "--quiet", "--initial-branch=rolling")
	runGit(t, seedDir, "config", "user.email", "update-test@example.invalid")
	runGit(t, seedDir, "config", "user.name", "update test")
	runGit(t, seedDir, "add", "-A")
	runGit(t, seedDir, "commit", "--quiet", "-m", "initial")
	commitA := testGitOutput(t, seedDir, "rev-parse", "HEAD")

	// Remote: bare repo.
	runGit(t, root, "init", "--bare", remoteDir)
	runGit(t, remoteDir, "symbolic-ref", "HEAD", "refs/heads/rolling")
	runGit(t, seedDir, "remote", "add", "origin", remoteDir)
	runGit(t, seedDir, "push", "--quiet", "-u", "origin", "rolling")

	// Install through the manager.
	m := NewManager(Options{Home: home, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	receipt, err := m.Install(context.Background(), seedDir, prefix)
	if err != nil {
		t.Fatalf("Install failed: %v", err)
	}
	if receipt.InstalledCommit != commitA {
		t.Fatalf("installed commit = %q, want %q", receipt.InstalledCommit, commitA)
	}

	// Verify the initial binary is a symlink into the profile.
	got, err := os.Readlink(binaryPath)
	if err != nil {
		t.Fatalf("binary at %s is not a symlink after install: %v", binaryPath, err)
	}
	wantTarget := filepath.Join(m.paths.Profile, "bin", "mct-agent")
	if got != wantTarget {
		t.Fatalf("symlink target = %q, want %q", got, wantTarget)
	}

	// Simulate a stale install: replace the symlink with a regular file.
	oldContent, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(binaryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Readlink(binaryPath); err == nil {
		t.Fatal("expected binary to be a regular file after replacement")
	}

	// Advance the remote.
	runGit(t, seedDir, "commit", "--quiet", "--allow-empty", "-m", "update")
	commitB := testGitOutput(t, seedDir, "rev-parse", "HEAD")
	runGit(t, seedDir, "push", "--quiet", "origin", "rolling")

	// Check and update.
	result, err := m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusAvailable {
		t.Fatalf("expected update available, got status=%s", result.Status)
	}
	result, err = m.Update(context.Background(), result)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if result.Status != StatusUpdated {
		t.Fatalf("expected StatusUpdated, got status=%s", result.Status)
	}
	if result.CurrentCommit != commitB {
		t.Fatalf("update commit = %q, want %q", result.CurrentCommit, commitB)
	}

	// RED assertion: after update the binary must be a symlink again.
	gotTarget, readErr := os.Readlink(binaryPath)
	if readErr != nil {
		// BUG: Update never re-asserted the binary symlink, so after
		// replacing it with a regular file it stays a regular file.
		t.Fatalf("binary at %s must be a symlink after update but is not: %v", binaryPath, readErr)
	}
	if gotTarget != wantTarget {
		t.Fatalf("symlink target = %q, want %q", gotTarget, wantTarget)
	}
}

func findProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "flake.nix")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func copyDir(t *testing.T, src, dst string, skip ...string) {
	t.Helper()
	excludeArgs := []string{"-a", "--exclude=.git", "--exclude=.gocache"}
	for _, s := range skip {
		excludeArgs = append(excludeArgs, "--exclude="+s)
	}
	excludeArgs = append(excludeArgs, src+"/", dst+"/")
	if out, err := exec.Command("rsync", excludeArgs...).CombinedOutput(); err != nil {
		t.Fatalf("copyDir rsync: %v\n%s", err, out)
	}
}

func TestAtomicSymlinkCreatesDestinationDirectory(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "profile", "bin", "mct-agent")
	destination := filepath.Join(root, "prefix", "bin", "mct-agent")
	if err := atomicSymlink(target, destination); err != nil {
		t.Fatal(err)
	}
	got, err := os.Readlink(destination)
	if err != nil {
		t.Fatal(err)
	}
	if got != target {
		t.Fatalf("symlink target = %q, want %q", got, target)
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
