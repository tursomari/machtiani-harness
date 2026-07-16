package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
)

func buildBinary(t *testing.T, tags ...string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	args := []string{"build", "-buildvcs=false", "-o"}
	tmpDir := t.TempDir()
	bin := filepath.Join(tmpDir, "file-discovery")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	args = append(args, bin)
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./cmd/file-discovery")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	// Build from repo root (this test runs from ./e2e)
	cwd, _ := os.Getwd()
	cmd.Dir = filepath.Dir(cwd)
	cmd.Env = append(os.Environ(), "GOCACHE="+goCacheDir(tmpDir))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

func goCacheDir(fallbackRoot string) string {
	if inherited := strings.TrimSpace(os.Getenv("GOCACHE")); inherited != "" {
		return inherited
	}
	return filepath.Join(fallbackRoot, ".gocache")
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
	mustWrite("README.md", []byte("hello\n"))
	mustWrite("cmd/main.go", []byte("package main\nfunc main(){}\n"))
	mustWrite("internal/x.go", []byte("package internal\n"))
	mustWrite(".env", []byte("FOO=bar\n"))
	mustWrite(".git/keep", []byte(""))
	mustWrite("node_modules/pkg/index.js", []byte("console.log('x')\n"))
	mustWrite("assets/logo.png", make([]byte, 128))
	mustWrite("docs/spec.pdf", make([]byte, 128))
	return dir
}

type runResult struct {
	stdout, stderr string
	code           int
}

func runBin(t *testing.T, bin, cwd string, args []string, env []string) runResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = cwd
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = bytes.NewReader(nil)
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

func baseEnv(t *testing.T) []string {
	env := os.Environ()
	// Ensure isolation from user credentials and prove the discovery binary does
	// not depend on rg, sed, ls, or any other executable at runtime.
	for _, k := range []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL"} {
		env = append(env, k+"=")
	}
	env = append(env, "PATH="+t.TempDir())
	return env
}

func TestDryRun_RGOnly(t *testing.T) {
	bin := buildBinary(t)
	repo := makeFixtureRepo(t)
	env := append(baseEnv(t), "FILE_DISCOVERY_TRAJECTORY=")
	res := runBin(t, bin, repo, []string{"-dry-run-rg", "-no-trajectory"}, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	if res.stdout != "" {
		t.Fatalf("expected empty stdout, got: %q", res.stdout)
	}
	if !strings.Contains(res.stderr, "RG_OUT:\n") || !strings.Contains(res.stderr, "END_RG_OUT") {
		t.Fatalf("missing RG_OUT block in stderr: %s", res.stderr)
	}
	if !strings.Contains(res.stderr, ".env") {
		t.Fatalf("expected hidden files to be present; got:\n%s", res.stderr)
	}
	if strings.Contains(res.stderr, ".git/keep") || strings.Contains(res.stderr, "node_modules/") || strings.Contains(res.stderr, "logo.png") || strings.Contains(res.stderr, "spec.pdf") {
		t.Fatalf("expected excludes applied; got:\n%s", res.stderr)
	}
}

func TestDryRun_Pattern(t *testing.T) {
	bin := buildBinary(t)
	repo := makeFixtureRepo(t)
	env := append(baseEnv(t), "FILE_DISCOVERY_TRAJECTORY=")
	res := runBin(t, bin, repo, []string{"-dry-run-rg", "-pattern", `go|README`, "-no-trajectory"}, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	if strings.Contains(res.stderr, "README.md") && strings.Contains(res.stderr, "cmd/main.go") && strings.Contains(res.stderr, "internal/x.go") {
		// ok
	} else {
		t.Fatalf("expected only matching paths; got:\n%s", res.stderr)
	}
	// invalid regex
	res2 := runBin(t, bin, repo, []string{"-dry-run-rg", "-pattern", "[", "-no-trajectory"}, env)
	if res2.code == 0 || !strings.Contains(res2.stderr, "Invalid pattern:") {
		t.Fatalf("expected invalid pattern path to fail; exit=%d stderr=%s", res2.code, res2.stderr)
	}
}

func TestDryRun_CmdTimeout(t *testing.T) {
	// Build with a tag that makes native enumeration block until timeout.
	bin := buildBinary(t, "e2e_slow_rg")
	repo := makeFixtureRepo(t)
	env := append(baseEnv(t), "FILE_DISCOVERY_TRAJECTORY=")
	res := runBin(t, bin, repo, []string{"-dry-run-rg", "-cmd-timeout", "1", "-no-trajectory"}, env)
	if res.code == 0 || !strings.Contains(res.stderr, "Command timed out after 1s") {
		t.Fatalf("expected timeout; exit=%d stderr=%s", res.code, res.stderr)
	}
}

func TestDryRun_MaxStdout(t *testing.T) {
	bin := buildBinary(t)
	repo := makeFixtureRepo(t)
	env := append(baseEnv(t), "FILE_DISCOVERY_TRAJECTORY=")
	res := runBin(t, bin, repo, []string{"-dry-run-rg", "-max-stdout", "10", "-no-trajectory"}, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	if !strings.Contains(res.stderr, "[TRUNCATED:") {
		t.Fatalf("expected truncation footer; stderr=\n%s", res.stderr)
	}
}

func TestDryRun_TrajectoryFile(t *testing.T) {
	bin := buildBinary(t)
	repo := makeFixtureRepo(t)
	dir := t.TempDir()
	traj := filepath.Join(dir, "traj.jsonl")
	env := baseEnv(t)
	res := runBin(t, bin, repo, []string{"-dry-run-rg", "-trajectory", traj}, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	// Assert file exists and contains required events
	b, err := os.ReadFile(traj)
	if err != nil {
		t.Fatalf("trajectory read: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	have := map[string]bool{}
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		if typ, ok := m["type"].(string); ok {
			have[typ] = true
		}
	}
	for _, need := range []string{"run_start", "round_start", "rg_exec", "rg_out_emitted", "run_end"} {
		if !have[need] {
			t.Fatalf("missing trajectory event %q; have=%v", need, have)
		}
	}
}

func TestDryRun_NoTrajectoryOverridesEnv(t *testing.T) {
	bin := buildBinary(t)
	repo := makeFixtureRepo(t)
	dir := t.TempDir()
	env := baseEnv(t)
	env = append(env, "FILE_DISCOVERY_TRAJECTORY="+filepath.Join(dir, "traj.jsonl"))
	res := runBin(t, bin, repo, []string{"-dry-run-rg", "-no-trajectory"}, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	// ensure no file created
	fis, _ := os.ReadDir(dir)
	if len(fis) != 0 {
		t.Fatalf("expected no trajectory file created; found %d entries", len(fis))
	}
}

func TestDryRun_NoJSONFlagSetsMode(t *testing.T) {
	bin := buildBinary(t)
	repo := makeFixtureRepo(t)
	dir := t.TempDir()
	traj := filepath.Join(dir, "traj.jsonl")
	env := baseEnv(t)
	res := runBin(t, bin, repo, []string{"-dry-run-rg", "-no-json", "-trajectory", traj}, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	data, err := os.ReadFile(traj)
	if err != nil {
		t.Fatalf("trajectory read: %v", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	found := false
	for sc.Scan() {
		var evt map[string]any
		if err := json.Unmarshal(sc.Bytes(), &evt); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		if typ, ok := evt["type"].(string); !ok || typ != "run_start" {
			continue
		}
		cfgVal, ok := evt["cfg"].(map[string]any)
		if !ok {
			t.Fatalf("run_start missing cfg map: %v", evt)
		}
		mode, ok := cfgVal["toolCallMode"].(string)
		if !ok {
			t.Fatalf("cfg missing toolCallMode: %v", cfgVal)
		}
		if mode != string(cfgpkg.ToolCallModeSimple) {
			t.Fatalf("expected toolCallMode simple, got %q", mode)
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("run_start event with toolCallMode not found in trajectory\n%s", string(data))
	}
}

func TestDryRun_LogJSON(t *testing.T) {
	bin := buildBinary(t)
	repo := makeFixtureRepo(t)
	env := append(baseEnv(t), "FILE_DISCOVERY_TRAJECTORY=")
	res := runBin(t, bin, repo, []string{"-dry-run-rg", "-log-json", "-no-trajectory"}, env)
	if res.code != 0 {
		t.Fatalf("exit=%d stderr=%s", res.code, res.stderr)
	}
	// Expect some JSON lines in stderr; not all lines are JSON because RG_OUT is printed too
	hasJSON := false
	sc := bufio.NewScanner(strings.NewReader(res.stderr))
	for sc.Scan() {
		ln := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(ln, "{") && strings.Contains(ln, "\"event\"") {
			hasJSON = true
			break
		}
	}
	if !hasJSON {
		t.Fatalf("expected JSON log lines in stderr; got:\n%s", res.stderr)
	}
	if !strings.Contains(res.stderr, "RG_OUT:\n") {
		t.Fatalf("expected human RG_OUT block even with -log-json; stderr=\n%s", res.stderr)
	}
}

func TestDryRun_IrrelevantFlagsIgnored(t *testing.T) {
	bin := buildBinary(t)
	repo := makeFixtureRepo(t)
	env := append(baseEnv(t), "FILE_DISCOVERY_TRAJECTORY=")
	base := runBin(t, bin, repo, []string{"-dry-run-rg", "-no-trajectory"}, env)
	withIrrelevant := runBin(t, bin, repo, []string{"-dry-run-rg", "-api-key", "foo", "-base-url", "http://example", "-model", "m", "-max-rounds", "1", "-max-transcript", "1", "-no-trajectory"}, env)
	// Compare only the RG_OUT bodies, as unordered sets, to avoid nondeterministic ordering
	baseSet := rgPathsSet(base.stderr)
	withSet := rgPathsSet(withIrrelevant.stderr)
	if strings.Join(baseSet, "\n") != strings.Join(withSet, "\n") {
		t.Fatalf("expected identical RG_OUT sets; base=%v with=%v", baseSet, withSet)
	}
}

func rgBlock(s string) string {
	start := strings.Index(s, "RG_OUT:\n")
	if start == -1 {
		return ""
	}
	end := strings.Index(s[start:], "END_RG_OUT")
	if end == -1 {
		return s[start:]
	}
	return s[start : start+end+len("END_RG_OUT")]
}

func rgPathsSet(s string) []string {
	// Extract the body strictly between header and footer to avoid glued markers
	start := strings.Index(s, "RG_OUT:\n")
	if start == -1 {
		return nil
	}
	body := s[start+len("RG_OUT:\n"):]
	if end := strings.Index(body, "END_RG_OUT"); end != -1 {
		body = body[:end]
	}
	var paths []string
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "[TRUNCATED:") {
			continue
		}
		paths = append(paths, ln)
	}
	sort.Strings(paths)
	return paths
}
