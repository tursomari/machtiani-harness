package environments

import (
	"context"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsVisibleFiles(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "Documents Ω", "Dear Machine proof")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	initWindowsRepo(t, repo)
	env, err := NewLocalEnvironment(&minisweagent.EnvironmentConfig{CommandTimeout: 15})
	if err != nil {
		t.Fatal(err)
	}
	result, err := env.Execute(context.Background(), `printf 'hello Windows\n' > 'résumé result.txt'; cat 'résumé result.txt'`, repo)
	if err != nil || result.ReturnCode != 0 {
		t.Fatalf("shell: %+v %v", result, err)
	}
	b, err := os.ReadFile(filepath.Join(repo, "résumé result.txt"))
	if err != nil || string(b) != "hello Windows\n" {
		t.Fatalf("native file: %q %v", b, err)
	}
}
func TestWindowsCancellationStopsNativeDescendants(t *testing.T) {
	env, err := NewLocalEnvironment(&minisweagent.EnvironmentConfig{CommandTimeout: 15})
	if err != nil {
		t.Fatal(err)
	}
	repo := initTestRepo(t)
	command := `powershell.exe -NoProfile -NonInteractive -Command '$p = Start-Process ping.exe -ArgumentList "-n 120 127.0.0.1" -WindowStyle Hidden -PassThru; Write-Output $p.Id; Wait-Process -Id $p.Id'`
	running, err := env.Start(context.Background(), command, repo)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Kill()
	output := waitForOutput(t, running, "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(output))
	if err != nil {
		t.Fatalf("native child PID %q: %v", output, err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err = running.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-running.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("shell cancellation hung")
	}
	status, err := windows.WaitForSingleObject(handle, 5000)
	if err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatalf("native descendant survived cancellation: %v %v", status, err)
	}
}

func initWindowsRepo(t *testing.T, path string) {
	t.Helper()
	cmd := exec.Command("git", "init", "--quiet")
	cmd.Dir = path
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", output, err)
	}
}
