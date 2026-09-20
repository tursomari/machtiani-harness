package environments

import (
	"context"
	"fmt"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
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

func TestWindowsDetachedWorkerSurvivesShell(t *testing.T) {
	env, err := NewLocalEnvironment(&minisweagent.EnvironmentConfig{CommandTimeout: 15})
	if err != nil {
		t.Fatal(err)
	}
	defer env.Close()
	repo := initTestRepo(t)
	marker := filepath.Join(repo, "worker-pid")
	quote := func(value string) string {
		return "'" + strings.ReplaceAll(filepath.ToSlash(value), "'", "'\"'\"'") + "'"
	}
	command := fmt.Sprintf("%s -test.run=^TestWindowsDetachedHelper$ -- launch %s", quote(os.Args[0]), quote(marker))
	result, err := env.Execute(context.Background(), command, repo)
	if err != nil || result.ReturnCode != 0 {
		t.Fatalf("launch: %+v %v", result, err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	defer windows.TerminateProcess(handle, 1)
	status, err := windows.WaitForSingleObject(handle, 300)
	if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("worker died with shell: %v %v", status, err)
	}
}
func TestWindowsDetachedHelper(t *testing.T) {
	if len(os.Args) < 5 || os.Args[len(os.Args)-3] != "--" {
		return
	}
	mode, marker := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if mode == "worker" {
		if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		return
	}
	if mode != "launch" {
		t.Fatalf("unexpected helper mode: %s", mode)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestWindowsDetachedHelper$", "--", "worker", marker)
	child.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	// Git Bash may already have moved this native helper out of the temporary
	// job. Match the worker launcher: request breakaway only when permitted.
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	if err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)), nil); err == nil && limits.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK != 0 {
		child.SysProcAttr.CreationFlags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			child.Process.Release()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	child.Process.Kill()
	child.Wait()
	t.Fatal("worker did not start")
}
