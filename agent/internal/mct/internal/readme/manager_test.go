package readme

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const systemPromptText = "You are Machtiani's internal documentation agent. Write a cohesive internal README that reflects the current system state for engineers. Incorporate material architectural or service updates implied by the context, but do not mention commits, hashes, diffs, or change logs. The README must stand on its own, stay under 600 words, and use markdown."

func TestComposeMCTPromptInitialGeneration(t *testing.T) {
	dynamicContext := "Current commit (context only): abc123\n"
	got := composeMCTPrompt(systemPromptText, dynamicContext, "")
	if got != systemPromptText {
		t.Fatalf("expected only system prompt for initial generation, got %q", got)
	}
}

func TestComposeMCTPromptIncrementalGeneration(t *testing.T) {
	dynamicContext := "Current commit (context only): abc123\nPrevious internal README: ..."
	got := composeMCTPrompt(systemPromptText, dynamicContext, "abc123")
	expected := systemPromptText + "\n\n" + dynamicContext
	if got != expected {
		t.Fatalf("expected system prompt plus context, got %q", got)
	}
}

func TestComposeMCTPromptIncrementalWithoutContext(t *testing.T) {
	dynamicContext := "\n"
	got := composeMCTPrompt(systemPromptText, dynamicContext, "abc123")
	if got != systemPromptText {
		t.Fatalf("expected system prompt when dynamic context empty, got %q", got)
	}
}

func TestReadREADMEForProjectIgnoresMutableWorktree(t *testing.T) {
	repo := initReadmeTestRepo(t)
	projectCommit := strings.Repeat("a", 40)
	writeReadmeTestFile(t, repo, "tagged background\n")
	runReadmeTestGit(t, repo, "add", readmeFilename)
	runReadmeTestGit(t, repo, "commit", "-m", "tagged version")
	taggedCommit := strings.TrimSpace(runReadmeTestGit(t, repo, "rev-parse", "HEAD"))
	runReadmeTestGit(t, repo, "tag", "-a", "oid-"+projectCommit, "-m", "mapping")

	writeReadmeTestFile(t, repo, "newer README head\n")
	runReadmeTestGit(t, repo, "add", readmeFilename)
	runReadmeTestGit(t, repo, "commit", "-m", "newer version")
	writeReadmeTestFile(t, repo, "uncommitted shared artifact\n")

	content, readmeCommit, err := readREADMEForProjectAt(repo, projectCommit)
	if err != nil {
		t.Fatalf("read README for project: %v", err)
	}
	if content != "tagged background\n" {
		t.Fatalf("content = %q, want tagged background", content)
	}
	if readmeCommit != taggedCommit {
		t.Fatalf("README commit = %q, want %q", readmeCommit, taggedCommit)
	}
	data, err := os.ReadFile(filepath.Join(repo, readmeFilename))
	if err != nil {
		t.Fatalf("read mutable artifact: %v", err)
	}
	if string(data) != "uncommitted shared artifact\n" {
		t.Fatalf("immutable read unexpectedly changed worktree to %q", data)
	}
}

func TestCheckoutReadonlyReadmeMaterializesTaggedObject(t *testing.T) {
	repo := initReadmeTestRepo(t)
	projectCommit := strings.Repeat("b", 40)
	writeReadmeTestFile(t, repo, "tagged background\n")
	runReadmeTestGit(t, repo, "add", readmeFilename)
	runReadmeTestGit(t, repo, "commit", "-m", "tagged version")
	runReadmeTestGit(t, repo, "tag", "-a", "oid-"+projectCommit, "-m", "mapping")
	writeReadmeTestFile(t, repo, "stale artifact\n")

	if err := checkoutReadonlyReadmeAt(repo, projectCommit); err != nil {
		t.Fatalf("materialize README: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(repo, readmeFilename))
	if err != nil {
		t.Fatalf("read materialized README: %v", err)
	}
	if string(data) != "tagged background\n" {
		t.Fatalf("materialized content = %q, want tagged background", data)
	}
}

func TestTagREADMEWithProjectCommitIsIdempotentAndImmutable(t *testing.T) {
	repo := initReadmeTestRepo(t)
	projectCommit := strings.Repeat("c", 40)
	writeReadmeTestFile(t, repo, "mapped README\n")
	runReadmeTestGit(t, repo, "add", readmeFilename)
	runReadmeTestGit(t, repo, "commit", "-m", "mapped version")

	if err := tagREADMEWithProjectCommitAt(repo, projectCommit); err != nil {
		t.Fatalf("create README tag: %v", err)
	}
	if err := tagREADMEWithProjectCommitAt(repo, projectCommit); err != nil {
		t.Fatalf("repeat README tag: %v", err)
	}
	mappedCommit := strings.TrimSpace(runReadmeTestGit(t, repo, "rev-parse", "oid-"+projectCommit+"^{commit}"))

	writeReadmeTestFile(t, repo, "different README\n")
	runReadmeTestGit(t, repo, "add", readmeFilename)
	runReadmeTestGit(t, repo, "commit", "-m", "different version")
	if err := tagREADMEWithProjectCommitAt(repo, projectCommit); err == nil {
		t.Fatal("expected an existing project mapping to reject a different README commit")
	}
	stillMapped := strings.TrimSpace(runReadmeTestGit(t, repo, "rev-parse", "oid-"+projectCommit+"^{commit}"))
	if stillMapped != mappedCommit {
		t.Fatalf("README tag moved from %s to %s", mappedCommit, stillMapped)
	}
}

func TestAcquireSyncLockIsExclusive(t *testing.T) {
	stateDir := t.TempDir()
	manager := &Manager{StateDirPath: stateDir}
	first, err := manager.acquireSyncLock()
	if err != nil {
		t.Fatalf("acquire first sync lock: %v", err)
	}
	defer func() {
		_ = syscall.Flock(int(first.Fd()), syscall.LOCK_UN)
		_ = first.Close()
	}()

	second, err := os.OpenFile(filepath.Join(stateDir, syncLockFilename), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open second sync lock handle: %v", err)
	}
	defer second.Close()
	err = syscall.Flock(int(second.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("second lock error = %v, want EWOULDBLOCK", err)
	}
}

func initReadmeTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runReadmeTestGit(t, repo, "init")
	runReadmeTestGit(t, repo, "config", "user.name", "Test")
	runReadmeTestGit(t, repo, "config", "user.email", "test@example.com")
	return repo
}

func writeReadmeTestFile(t *testing.T, repo, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, readmeFilename), []byte(content), 0o644); err != nil {
		t.Fatalf("write test README: %v", err)
	}
}

func runReadmeTestGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
