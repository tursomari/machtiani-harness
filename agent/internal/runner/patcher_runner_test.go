package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()
	stubSrc := []byte(`package main
import ("io"; "os")
func main(){ io.Copy(os.Stdout, os.Stdin) }
`)
	buildStub := func(name string) string {
		srcPath := filepath.Join(dir, name+".go")
		if err := os.WriteFile(srcPath, stubSrc, 0o644); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", bin, srcPath)
		cmd.Env = os.Environ()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("skipping build-dependent test: %v (%s)", err, string(out))
		}
		return bin
	}
	envBin := buildStub("patcher-env")
	flagBin := buildStub("patcher-flag")
	pathBin := buildStub("patcher")

	originalPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", originalPath) })
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+originalPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}

	t.Setenv("PATCHER_BIN", "")
	pr := &PatcherRunner{}
	if err := pr.Resolve(""); err != nil {
		t.Fatalf("expected PATH patcher to resolve, got err: %v", err)
	}
	if pr.exePath != pathBin {
		t.Fatalf("expected PATH patcher (%s), got %s", pathBin, pr.exePath)
	}

	t.Setenv("PATCHER_BIN", envBin)
	if err := pr.Resolve(""); err != nil {
		t.Fatalf("expected env patcher to resolve, got err: %v", err)
	}
	if pr.exePath != envBin {
		t.Fatalf("expected env exePath, got %s", pr.exePath)
	}

	if err := pr.Resolve(flagBin); err != nil {
		t.Fatalf("expected flag patcher to resolve, got err: %v", err)
	}
	if pr.exePath != flagBin {
		t.Fatalf("expected flag exePath, got %s", pr.exePath)
	}
}

func TestRunJSON_DryRun(t *testing.T) {
	pr := &PatcherRunner{DryRun: true, SessionID: "sess"}
	if err := pr.Resolve(""); err != nil {
		t.Fatalf("resolve dry-run: %v", err)
	}
	out, errOut, err := pr.RunJSON(context.Background(), []byte("{}"), false)
	if err != nil || string(errOut) != "" || string(out) == "" {
		t.Fatalf("dry-run exec mismatch: out=%q errOut=%q err=%v", string(out), string(errOut), err)
	}
}

func TestRunJSON_Executes(t *testing.T) {
	// Create a temporary stub binary that echoes stdin to stdout and logs to stderr.
	dir := t.TempDir()
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
	binPath := filepath.Join(dir, "patcher-stub")
	// Build the stub
	cmd := exec.Command("go", "build", "-o", binPath, srcPath)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("skipping build-dependent test on %s: %v (%s)", runtime.GOOS, err, string(out))
		return
	}
	pr := &PatcherRunner{SessionID: "sess"}
	if err := pr.Resolve(binPath); err != nil {
		t.Fatal(err)
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
