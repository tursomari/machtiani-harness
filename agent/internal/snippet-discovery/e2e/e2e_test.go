package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type runResult struct {
	stdout string
	stderr string
	code   int
}

func buildBinary(t *testing.T, tags ...string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	args := []string{"build", "-o"}
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "snippet-discovery")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	args = append(args, bin)
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./cmd/snippet-discovery")
	cmd := exec.Command("go", args...)
	cwd, _ := os.Getwd()
	cmd.Dir = filepath.Dir(cwd)
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(tmpDir, ".gocache"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func runBin(t *testing.T, bin, cwd string, args []string, env []string) runResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Dir = cwd
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run failed: %v\nSTDERR:%s\nSTDOUT:%s", err, stderr.String(), stdout.String())
		}
	}
	return runResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}

func baseEnv() []string {
	env := os.Environ()
	for _, k := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL", "SNIPPET_DISCOVERY_E2E_RESPONSES"} {
		env = append(env, k+"=")
	}
	return env
}

func makeFixtureRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite := func(path string, data []byte) {
		full := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	mustWrite("foo.txt", []byte("alpha\nbeta\ngamma\n"))
	return dir
}

func TestSnippetDiscoveryBasic(t *testing.T) {
	bin := buildBinary(t, "e2e_stub_llm")
	repo := makeFixtureRepo(t)
	responses := "<show>\nfoo.txt\n</show>|{\"foo.txt\":[{\"start\":1,\"end\":2}]}"
	env := append(baseEnv(),
		"SNIPPET_DISCOVERY_E2E_RESPONSES="+responses,
	)
	args := []string{
		"-r", "find alpha",
		"-f", "foo.txt",
		"-openai-api-key", "test",
		"-openai-base-url", "http://example",
		"-openai-model", "test",
		"-no-trajectory",
	}
	res := runBin(t, bin, repo, args, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	var parsed map[string][]map[string]int
	if err := json.Unmarshal([]byte(res.stdout), &parsed); err != nil {
		t.Fatalf("invalid json output: %v\n%s", err, res.stdout)
	}
	if len(parsed["foo.txt"]) != 1 || parsed["foo.txt"][0]["start"] != 1 {
		t.Fatalf("unexpected output: %v", parsed)
	}
}

func TestSnippetDiscoveryInvalidPath(t *testing.T) {
	bin := buildBinary(t, "e2e_stub_llm")
	repo := makeFixtureRepo(t)
	args := []string{
		"-r", "missing file",
		"-f", "missing.txt",
		"-openai-api-key", "test",
		"-openai-base-url", "http://example",
		"-openai-model", "test",
		"-no-trajectory",
	}
	res := runBin(t, bin, repo, args, baseEnv())
	if res.code == 0 {
		t.Fatalf("expected non-zero exit for invalid path")
	}
}

func TestSnippetDiscoveryTrajectory(t *testing.T) {
	bin := buildBinary(t, "e2e_stub_llm")
	repo := makeFixtureRepo(t)
	responses := "{\"foo.txt\":[{\"start\":1,\"end\":1}]}"
	traj := filepath.Join(t.TempDir(), "traj.jsonl")
	env := append(baseEnv(), "SNIPPET_DISCOVERY_E2E_RESPONSES="+responses)
	args := []string{
		"-r", "direct finalize",
		"-f", "foo.txt",
		"-openai-api-key", "test",
		"-openai-base-url", "http://example",
		"-openai-model", "test",
		"-trajectory", traj,
	}
	res := runBin(t, bin, repo, args, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	if _, err := os.Stat(traj); err != nil {
		t.Fatalf("trajectory missing: %v", err)
	}
}
