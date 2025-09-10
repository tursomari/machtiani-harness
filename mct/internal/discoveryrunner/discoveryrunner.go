package discoveryrunner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Result holds parsed file paths from file-discovery output
type Result struct {
	Paths []string
}

// Matches blocks like:
// BEGIN_RELEVANT_FILES[session]
// <paths...>
// END_RELEVANT_FILES[session]
// Go's regexp doesn't support backreferences, so capture both session ids and compare in code.
var blockRe = regexp.MustCompile(`(?s)BEGIN_RELEVANT_FILES\[(.+?)\]\n(.*?)\nEND_RELEVANT_FILES\[(.+?)\]`)

// findBinary resolves the file-discovery binary path using precedence:
// 1) FILE_DISCOVERY_BIN env
// 2) file-discovery in PATH
// 3) <exec-dir>/bin/file-discovery
func findBinary() (string, error) {
	if override := os.Getenv("FILE_DISCOVERY_BIN"); override != "" {
		return override, nil
	}
	if p, err := exec.LookPath("file-discovery"); err == nil {
		return p, nil
	}
	exe, err := os.Executable()
	if err == nil {
		base := filepath.Dir(exe)
		candidate := filepath.Join(base, "bin", "file-discovery")
		if st, err2 := os.Stat(candidate); err2 == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("file-discovery binary not found; set FILE_DISCOVERY_BIN or add to PATH")
}

// isSafePath performs defense-in-depth checks on a relative file path
func isSafePath(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return false
	}
	if strings.Contains(p, "\\") { // no backslashes
		return false
	}
	if strings.Contains(p, "..") { // disallow parent traversal
		return false
	}
	if strings.HasSuffix(p, "/") { // no trailing slash directories
		return false
	}
	clean := filepath.Clean(p)
	if strings.HasPrefix(clean, "..") { // extra guard after Clean
		return false
	}
	return true
}

// uniqOrder deduplicates while preserving order
func uniqOrder(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// Run executes file-discovery with the given prompt and environment mapping.
// It returns the list of discovered relative paths.
func Run(ctx context.Context, prompt, model, apiKey, baseURL, sessionID string) (Result, error) {
	bin, err := findBinary()
	if err != nil {
		return Result{}, err
	}

	args := []string{}
	if sessionID != "" {
		args = append(args, "-session-id", sessionID)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	// Build env with OPENAI_* expected by file-discovery
	env := os.Environ()
	if apiKey != "" {
		env = append(env, "OPENAI_API_KEY="+apiKey)
	}
	if baseURL != "" {
		env = append(env, "OPENAI_BASE_URL="+baseURL)
	}
	if model != "" {
		env = append(env, "OPENAI_MODEL="+model)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr // explicitly ignore logs; useful for debug on failure

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, fmt.Errorf("failed to open stdin: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("failed to start file-discovery: %w", err)
	}

	// Stream prompt into stdin
	go func() {
		w := bufio.NewWriter(stdin)
		_, _ = w.WriteString(prompt)
		_ = w.Flush()
		_ = stdin.Close()
	}()

	if err := cmd.Wait(); err != nil {
		// include a hint with stderr for troubleshooting
		return Result{}, fmt.Errorf("file-discovery failed: %w\n%s", err, stderr.String())
	}

	outStr := stdout.String()
	m := blockRe.FindStringSubmatch(outStr)
	if len(m) != 4 {
		return Result{}, errors.New("failed to parse relevant files block from file-discovery output")
	}
	if m[1] != m[3] {
		return Result{}, errors.New("mismatched session ids in relevant files block")
	}

	block := m[2]
	lines := strings.Split(block, "\n")
	var paths []string
	for _, line := range lines {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		if !isSafePath(p) {
			continue
		}
		paths = append(paths, p)
	}

	paths = uniqOrder(paths)
	return Result{Paths: paths}, nil
}
