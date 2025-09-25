package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func buildPatcherStub(t *testing.T, dir string) string {
	t.Helper()
	src := []byte(`package main
import (
  "fmt"; "io"; "os"
)
func main(){
  fmt.Fprintln(os.Stderr, "stub patcher running")
  io.Copy(os.Stdout, os.Stdin)
}
`)
	srcPath := filepath.Join(dir, "main.go")
	if err := os.WriteFile(srcPath, src, 0o644); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, "patcher")
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("skipping build-dependent test on %s: %v (%s)", runtime.GOOS, err, string(out))
	}
	return binPath
}

func TestResolvePrefersPATH(t *testing.T) {
	dir := t.TempDir()
	binPath := buildPatcherStub(t, dir)
	t.Setenv("PATH", dir)

	pr := &PatcherRunner{}
	if err := pr.Resolve(); err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if pr.exePath != binPath {
		t.Fatalf("expected %s, got %s", binPath, pr.exePath)
	}
}

func TestResolveMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)

	pr := &PatcherRunner{}
	if err := pr.Resolve(); err == nil {
		t.Fatalf("expected resolve error when patcher missing")
	}
}

func TestRunJSON_DryRun(t *testing.T) {
	pr := &PatcherRunner{DryRun: true, SessionID: "sess"}
	if err := pr.Resolve(); err != nil {
		t.Fatalf("resolve dry-run: %v", err)
	}
	out, errOut, err := pr.RunJSON(context.Background(), []byte("{}"), false)
	if err != nil || string(errOut) != "" || string(out) == "" {
		t.Fatalf("dry-run exec mismatch: out=%q errOut=%q err=%v", string(out), string(errOut), err)
	}
}

func TestRunJSON_Executes(t *testing.T) {
	dir := t.TempDir()
	binPath := buildPatcherStub(t, dir)
	t.Setenv("PATH", dir)

	pr := &PatcherRunner{SessionID: "sess"}
	if err := pr.Resolve(); err != nil {
		t.Fatal(err)
	}
	if pr.exePath != binPath {
		t.Fatalf("expected resolved path %s, got %s", binPath, pr.exePath)
	}
	out, errOut, err := pr.RunJSON(context.Background(), []byte(`{"k":"v"}`), true)
	if err != nil {
		t.Fatalf("stub exec err: %v", err)
	}
	if string(out) != `{"k":"v"}` {
		t.Fatalf("stdout mismatch: %q", string(out))
	}
	if len(errOut) == 0 {
		t.Fatalf("expected stderr output from stub")
	}
}
