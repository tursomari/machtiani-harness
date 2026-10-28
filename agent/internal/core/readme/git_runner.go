package readme

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	controlledGitConfigFilename = "gitconfig"
	emptyHooksDirName           = "empty-hooks"
	emptyAttributesFilename     = "empty-attributes"
	emptyExcludesFilename       = "empty-excludes"
)

type gitRunner struct {
	repoPath string
	stateDir string
	env      []string
}

func newReadmeGitRunner(readmeRepoPath string) *gitRunner {
	repoPath := strings.TrimSpace(readmeRepoPath)
	if repoPath != "" {
		if abs, err := filepath.Abs(repoPath); err == nil {
			repoPath = abs
		} else {
			repoPath = filepath.Clean(repoPath)
		}
	}

	stateDir := ""
	globalConfig := os.DevNull
	if repoPath != "" {
		stateDir = filepath.Join(repoPath, stateDirName)
		globalConfig = filepath.Join(stateDir, controlledGitConfigFilename)
	}

	return &gitRunner{
		repoPath: repoPath,
		stateDir: stateDir,
		env: sanitizeGitEnv(map[string]string{
			"GIT_CONFIG_GLOBAL":      globalConfig,
			"GIT_CONFIG_NOSYSTEM":    "1",
			"GIT_ATTR_NOSYSTEM":      "1",
			"GIT_TERMINAL_PROMPT":    "0",
			"GIT_PAGER":              "cat",
			"GIT_EDITOR":             "true",
			"GIT_NO_REPLACE_OBJECTS": "1",
			"GIT_OPTIONAL_LOCKS":     "0",
		}),
	}
}

func sanitizeGitEnv(extra map[string]string) []string {
	values := make(map[string]string, len(os.Environ())+len(extra))
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "GIT_") || upper == "EMAIL" {
			continue
		}
		values[upper] = key + "=" + value
	}
	for key, value := range extra {
		upper := strings.ToUpper(key)
		values[upper] = upper + "=" + value
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, values[key])
	}
	return env
}

func (r *gitRunner) attributesFile() string {
	if r == nil || r.stateDir == "" {
		return os.DevNull
	}
	return filepath.Join(r.stateDir, emptyAttributesFilename)
}

func (r *gitRunner) excludesFile() string {
	if r == nil || r.stateDir == "" {
		return os.DevNull
	}
	return filepath.Join(r.stateDir, emptyExcludesFilename)
}

func (r *gitRunner) hooksPath() string {
	if r == nil || r.stateDir == "" {
		return os.DevNull
	}
	return filepath.Join(r.stateDir, emptyHooksDirName)
}

func (r *gitRunner) configArgs() []string {
	args := make([]string, 0, 40)
	add := func(key, value string) {
		args = append(args, "-c", key+"="+value)
	}

	if r != nil && r.repoPath != "" {
		add("safe.directory", r.repoPath)
	}
	add("user.name", readmeBotName)
	add("user.email", readmeBotEmail)
	add("author.name", readmeBotName)
	add("author.email", readmeBotEmail)
	add("committer.name", readmeBotName)
	add("committer.email", readmeBotEmail)
	add("commit.gpgSign", "false")
	add("tag.gpgSign", "false")
	add("core.hooksPath", r.hooksPath())
	add("core.attributesFile", r.attributesFile())
	add("core.excludesFile", r.excludesFile())
	add("core.autocrlf", "false")
	add("core.fsmonitor", "false")
	add("core.eol", "lf")
	add("core.safecrlf", "false")
	add("color.ui", "false")
	add("log.showSignature", "false")
	add("log.mailmap", "false")
	if runtime.GOOS == "windows" {
		add("core.longpaths", "true")
	}
	add("init.defaultBranch", "main")
	return args
}

func (r *gitRunner) command(workDir string, args ...string) *exec.Cmd {
	gitArgs := append(r.configArgs(), args...)
	cmd := exec.Command("git", gitArgs...)
	cmd.Dir = workDir
	cmd.Env = r.env
	return cmd
}

func (r *gitRunner) run(workDir string, args ...string) (string, error) {
	cmd := r.command(workDir, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (m *Manager) ensureControlledGitState() error {
	if strings.TrimSpace(m.ReadmeRepoPath) == "" || strings.TrimSpace(m.StateDirPath) == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(m.ReadmeRepoPath, ".git")); err != nil {
		return fmt.Errorf("internal README repo is not initialized: %w", err)
	}
	if err := os.MkdirAll(m.StateDirPath, 0o755); err != nil {
		return fmt.Errorf("create controlled Git state directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(m.StateDirPath, emptyHooksDirName), 0o700); err != nil {
		return fmt.Errorf("create controlled Git hooks directory: %w", err)
	}
	for _, name := range []string{
		controlledGitConfigFilename,
		emptyAttributesFilename,
		emptyExcludesFilename,
	} {
		path := filepath.Join(m.StateDirPath, name)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return fmt.Errorf("write controlled Git state %s: %w", path, err)
		}
	}
	return nil
}
