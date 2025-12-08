package patcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
)

func TestEnsureBaselineCapturesFilesLazily(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initGitRepo(t, repo)
	touched := filepath.Join(repo, "captured.txt")
	untouched := filepath.Join(repo, "untouched.txt")
	if err := os.WriteFile(touched, []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("write touched file: %v", err)
	}
	if err := os.WriteFile(untouched, []byte("stay put\n"), 0o644); err != nil {
		t.Fatalf("write untouched file: %v", err)
	}

	sessionID := fmt.Sprintf("lazy-%d", time.Now().UnixNano())
	state, err := EnsureBaseline(sessionID, repo, time.Now())
	if err != nil {
		t.Fatalf("EnsureBaseline: %v", err)
	}
	sessionDir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("session directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	if got := len(state.Manifest.Files); got != 0 {
		t.Fatalf("expected empty manifest, got %d entries", got)
	}

	baselineTouched := state.FilePath("captured.txt")
	if _, err := os.Stat(baselineTouched); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected baseline copy to not exist, got err=%v", err)
	}

	if err := state.VerifyFiles([]string{"captured.txt", "captured.txt"}); err != nil {
		t.Fatalf("VerifyFiles: %v", err)
	}

	rec, ok := state.ManifestEntry("captured.txt")
	if !ok {
		t.Fatalf("expected manifest entry for captured.txt")
	}
	if want := "captured.txt"; strings.TrimSpace(rec.Path) != want {
		t.Fatalf("unexpected manifest path: %q", rec.Path)
	}
	if rec.Size == 0 {
		t.Fatalf("expected recorded size to be non-zero")
	}

	if data, err := os.ReadFile(baselineTouched); err != nil {
		t.Fatalf("read captured copy: %v", err)
	} else if string(data) != "hello world\n" {
		t.Fatalf("unexpected baseline contents: %q", data)
	}

	if _, ok := state.ManifestEntry("untouched.txt"); ok {
		t.Fatalf("unexpected manifest entry for untouched file")
	}
	if _, err := os.Stat(state.FilePath("untouched.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected untouched baseline file to be absent, got err=%v", err)
	}

	manifestBytes, err := os.ReadFile(state.ManifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest BaselineManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if got := len(manifest.Files); got != 1 {
		t.Fatalf("expected manifest to have exactly one entry, got %d", got)
	}
	if manifest.Files[0].Path != "captured.txt" {
		t.Fatalf("unexpected manifest path on disk: %q", manifest.Files[0].Path)
	}
}

func TestBuildBaselineDiffSectionTriggersLazyCapture(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	initGitRepo(t, repo)
	filePath := filepath.Join(repo, "notes.md")
	content := "alpha\nbravo\n"
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	sessionID := fmt.Sprintf("lazy-diff-%d", time.Now().UnixNano())
	state, err := EnsureBaseline(sessionID, repo, time.Now())
	if err != nil {
		t.Fatalf("EnsureBaseline: %v", err)
	}
	sessionDir, err := artifacts.SessionDirectory(sessionID)
	if err != nil {
		t.Fatalf("session directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sessionDir) })

	baselinePath := state.FilePath("notes.md")
	if _, err := os.Stat(baselinePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected baseline copy to be absent before diff, got err=%v", err)
	}

	section, included, err := BuildBaselineDiffSection(state, repo, "notes.md")
	if err != nil {
		t.Fatalf("BuildBaselineDiffSection: %v", err)
	}
	if !included {
		t.Fatalf("expected diff section to be included")
	}
	if !strings.Contains(section, "File: notes.md") {
		t.Fatalf("diff section did not mention file name:\n%s", section)
	}

	if data, err := os.ReadFile(baselinePath); err != nil {
		t.Fatalf("read baseline copy after diff: %v", err)
	} else if string(data) != content {
		t.Fatalf("unexpected baseline contents after diff: %q", data)
	}

	if _, ok := state.ManifestEntry("notes.md"); !ok {
		t.Fatalf("expected manifest entry for diffed file")
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}
