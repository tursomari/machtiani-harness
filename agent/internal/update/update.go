package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	receiptSchemaVersion = 1
	defaultRootRelative  = ".machtiani/installations/mct-agent"
	buildSubmodulePath   = "agent/internal/shell-agent"
)

type Policy string

const (
	PolicyPrompt Policy = "prompt"
	PolicyAuto   Policy = "auto"
	PolicyNotify Policy = "notify"
	PolicyOff    Policy = "off"
)

type Status string

const (
	StatusCurrent   Status = "current"
	StatusAvailable Status = "available"
	StatusUpdated   Status = "updated"
	StatusDeclined  Status = "declined"
)

type Paths struct {
	Root    string
	Source  string
	Receipt string
	Config  string
	State   string
	Lock    string
}

func PathsForHome(home string) Paths {
	root := filepath.Join(home, filepath.FromSlash(defaultRootRelative))
	return Paths{
		Root:    root,
		Source:  filepath.Join(root, "source"),
		Receipt: filepath.Join(root, "receipt.json"),
		Config:  filepath.Join(root, "update.toml"),
		State:   filepath.Join(root, "update-state.json"),
		Lock:    filepath.Join(root, "update.lock"),
	}
}

type Receipt struct {
	SchemaVersion    int       `json:"schema_version"`
	Remote           string    `json:"remote"`
	DefaultBranch    string    `json:"default_branch"`
	SourceDir        string    `json:"source_dir"`
	Prefix           string    `json:"prefix"`
	BinaryPath       string    `json:"binary_path"`
	InstalledCommit  string    `json:"installed_commit"`
	InstalledVersion string    `json:"installed_version"`
	InstalledAt      time.Time `json:"installed_at"`
}

type Config struct {
	Policy            Policy        `toml:"policy"`
	CooldownHours     int           `toml:"cooldown_hours"`
	FailureRetryHours int           `toml:"failure_retry_hours"`
	Cooldown          time.Duration `toml:"-"`
	FailureRetry      time.Duration `toml:"-"`
}

type State struct {
	LastAttemptAt  time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt  time.Time `json:"last_success_at,omitempty"`
	LastSeenCommit string    `json:"last_seen_commit,omitempty"`
}

type Result struct {
	Status          Status    `json:"status"`
	CurrentCommit   string    `json:"current_commit,omitempty"`
	CandidateCommit string    `json:"candidate_commit,omitempty"`
	Remote          string    `json:"remote,omitempty"`
	DefaultBranch   string    `json:"default_branch,omitempty"`
	SourceDir       string    `json:"source_dir,omitempty"`
	BinaryPath      string    `json:"binary_path,omitempty"`
	CheckedAt       time.Time `json:"checked_at"`
}

type Options struct {
	Home       string
	Version    string
	Commit     string
	Executable string
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Now        func() time.Time
}

type Manager struct {
	opts  Options
	paths Paths
}

func NewManager(opts Options) *Manager {
	if strings.TrimSpace(opts.Home) == "" {
		opts.Home, _ = os.UserHomeDir()
	}
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Manager{opts: opts, paths: PathsForHome(opts.Home)}
}

func (m *Manager) Paths() Paths { return m.paths }

func SaveReceipt(path string, receipt Receipt) error {
	if receipt.SchemaVersion == 0 {
		receipt.SchemaVersion = receiptSchemaVersion
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update receipt: %w", err)
	}
	return atomicWrite(path, append(data, '\n'), 0o600)
}

func LoadReceipt(path string) (Receipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("decode update receipt %s: %w", path, err)
	}
	if receipt.SchemaVersion != receiptSchemaVersion {
		return Receipt{}, fmt.Errorf("unsupported update receipt schema %d", receipt.SchemaVersion)
	}
	if strings.TrimSpace(receipt.Remote) == "" || strings.TrimSpace(receipt.SourceDir) == "" || strings.TrimSpace(receipt.BinaryPath) == "" || strings.TrimSpace(receipt.InstalledCommit) == "" {
		return Receipt{}, errors.New("update receipt is incomplete")
	}
	return receipt, nil
}

func defaultConfig() Config {
	return Config{Policy: PolicyPrompt, CooldownHours: 24, FailureRetryHours: 1, Cooldown: 24 * time.Hour, FailureRetry: time.Hour}
}

func LoadConfig(path string) (Config, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	var disk struct {
		Policy            Policy `toml:"policy"`
		CooldownHours     *int   `toml:"cooldown_hours"`
		FailureRetryHours *int   `toml:"failure_retry_hours"`
	}
	if err := toml.Unmarshal(data, &disk); err != nil {
		return Config{}, fmt.Errorf("decode update config %s: %w", path, err)
	}
	if disk.Policy != "" {
		cfg.Policy = disk.Policy
	}
	if disk.CooldownHours != nil {
		cfg.CooldownHours = *disk.CooldownHours
	}
	if disk.FailureRetryHours != nil {
		cfg.FailureRetryHours = *disk.FailureRetryHours
	}
	switch cfg.Policy {
	case PolicyPrompt, PolicyAuto, PolicyNotify, PolicyOff:
	default:
		return Config{}, fmt.Errorf("invalid update policy %q", cfg.Policy)
	}
	if cfg.CooldownHours < 0 || cfg.FailureRetryHours < 0 {
		return Config{}, errors.New("update cooldown hours must be non-negative")
	}
	cfg.Cooldown = time.Duration(cfg.CooldownHours) * time.Hour
	cfg.FailureRetry = time.Duration(cfg.FailureRetryHours) * time.Hour
	return cfg, nil
}

func saveDefaultConfig(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(path, []byte("policy = \"prompt\"\ncooldown_hours = 24\nfailure_retry_hours = 1\n"), 0o600)
}

func SanitizeRemote(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", errors.New("remote is empty")
	}
	parsed, err := url.Parse(remote)
	if err != nil {
		return "", fmt.Errorf("parse remote: %w", err)
	}
	if parsed.Scheme != "" {
		parsed.User = nil
		return parsed.String(), nil
	}
	// SCP-style SSH remotes may contain a username, but never a password or
	// token. Preserve them because git requires the username for SSH routing.
	return remote, nil
}

func ResolveRemoteHEAD(ctx context.Context, remote string) (branch, commit string, err error) {
	cleanRemote, sanitizeErr := SanitizeRemote(remote)
	if sanitizeErr != nil {
		return "", "", sanitizeErr
	}
	cmd := exec.CommandContext(ctx, "git", "ls-remote", "--symref", cleanRemote, "HEAD")
	output, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return "", "", fmt.Errorf("resolve remote HEAD for %s: %w: %s", cleanRemote, runErr, strings.TrimSpace(string(output)))
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			branch = strings.TrimPrefix(fields[1], "refs/heads/")
		}
		if len(fields) >= 2 && fields[1] == "HEAD" && fields[0] != "ref:" {
			commit = fields[0]
		}
	}
	if branch == "" || commit == "" {
		return "", "", fmt.Errorf("remote %s did not advertise a symbolic default branch", cleanRemote)
	}
	return branch, commit, nil
}

func (m *Manager) Check(ctx context.Context) (Result, error) {
	receipt, err := LoadReceipt(m.paths.Receipt)
	if err != nil {
		return Result{}, fmt.Errorf("load managed installation: %w", err)
	}
	branch, candidate, err := ResolveRemoteHEAD(ctx, receipt.Remote)
	now := m.opts.Now().UTC()
	if err != nil {
		_ = saveState(m.paths.State, State{LastAttemptAt: now})
		return Result{}, err
	}
	status := StatusAvailable
	if candidate == receipt.InstalledCommit {
		status = StatusCurrent
	}
	result := Result{Status: status, CurrentCommit: receipt.InstalledCommit, CandidateCommit: candidate, Remote: receipt.Remote, DefaultBranch: branch, SourceDir: receipt.SourceDir, BinaryPath: receipt.BinaryPath, CheckedAt: now}
	_ = saveState(m.paths.State, State{LastAttemptAt: now, LastSuccessAt: now, LastSeenCommit: candidate})
	return result, nil
}

func (m *Manager) Register(ctx context.Context, source, prefix string) (Receipt, error) {
	var err error
	source, err = filepath.Abs(source)
	if err != nil {
		return Receipt{}, err
	}
	prefix, err = filepath.Abs(prefix)
	if err != nil {
		return Receipt{}, err
	}
	if dirty, err := gitOutput(ctx, source, "status", "--porcelain"); err != nil {
		return Receipt{}, err
	} else if strings.TrimSpace(dirty) != "" {
		return Receipt{}, errors.New("managed source checkout is dirty")
	}
	remoteRaw, err := gitOutput(ctx, source, "remote", "get-url", "origin")
	if err != nil {
		return Receipt{}, errors.New("managed source requires an origin remote")
	}
	remote, err := SanitizeRemote(remoteRaw)
	if err != nil {
		return Receipt{}, err
	}
	branch, _, err := ResolveRemoteHEAD(ctx, remote)
	if err != nil {
		return Receipt{}, err
	}
	head, err := gitOutput(ctx, source, "rev-parse", "HEAD")
	if err != nil {
		return Receipt{}, err
	}
	if strings.TrimSpace(m.opts.Commit) != "" && m.opts.Commit != "unknown" && m.opts.Commit != head {
		return Receipt{}, fmt.Errorf("installed commit %s does not match source HEAD %s", m.opts.Commit, head)
	}
	binaryPath := filepath.Join(prefix, "bin", "mct-agent")
	if _, err := os.Stat(binaryPath); err != nil {
		return Receipt{}, fmt.Errorf("inspect installed binary %s: %w", binaryPath, err)
	}
	receipt := Receipt{SchemaVersion: receiptSchemaVersion, Remote: remote, DefaultBranch: branch, SourceDir: source, Prefix: prefix, BinaryPath: binaryPath, InstalledCommit: head, InstalledVersion: m.opts.Version, InstalledAt: m.opts.Now().UTC()}
	if err := SaveReceipt(m.paths.Receipt, receipt); err != nil {
		return Receipt{}, err
	}
	if err := saveDefaultConfig(m.paths.Config); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

// Update builds the exact candidate commit in an isolated worktree and only
// replaces the active binary after the candidate identifies itself correctly.
func (m *Manager) Update(ctx context.Context, result Result) (updated Result, retErr error) {
	if result.Status != StatusAvailable || strings.TrimSpace(result.CandidateCommit) == "" {
		return result, nil
	}
	lock, err := m.TryLock()
	if err != nil {
		return Result{}, fmt.Errorf("another update is already in progress: %w", err)
	}
	defer lock.Close()

	receipt, err := LoadReceipt(m.paths.Receipt)
	if err != nil {
		return Result{}, err
	}
	if err := m.ensureSource(ctx, receipt); err != nil {
		return Result{}, err
	}
	rawRemote, err := gitOutput(ctx, receipt.SourceDir, "remote", "get-url", "origin")
	if err != nil {
		return Result{}, err
	}
	cleanRemote, err := SanitizeRemote(rawRemote)
	if err != nil {
		return Result{}, err
	}
	if cleanRemote != receipt.Remote {
		return Result{}, fmt.Errorf("managed source origin %s does not match receipt remote %s", cleanRemote, receipt.Remote)
	}
	if dirty, err := gitOutput(ctx, receipt.SourceDir, "status", "--porcelain", "--untracked-files=all"); err != nil {
		return Result{}, err
	} else if dirty != "" {
		return Result{}, errors.New("managed source checkout contains local changes; preserve or remove them before updating")
	}
	sourceHead, err := gitOutput(ctx, receipt.SourceDir, "rev-parse", "HEAD")
	if err != nil {
		return Result{}, err
	}
	sourceBranch, _ := gitOutput(ctx, receipt.SourceDir, "symbolic-ref", "--quiet", "--short", "HEAD")

	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", result.DefaultBranch, result.DefaultBranch)
	if _, err := gitOutput(ctx, receipt.SourceDir, "fetch", "--depth=1", "origin", refspec); err != nil {
		return Result{}, err
	}
	fetched, err := gitOutput(ctx, receipt.SourceDir, "rev-parse", "refs/remotes/origin/"+result.DefaultBranch)
	if err != nil {
		return Result{}, err
	}
	if fetched != result.CandidateCommit {
		return Result{}, fmt.Errorf("remote moved during update: expected %s, fetched %s; retry", result.CandidateCommit, fetched)
	}

	if err := os.MkdirAll(receipt.Prefix, 0o755); err != nil {
		return Result{}, err
	}
	stageRoot, err := os.MkdirTemp(receipt.Prefix, ".mct-agent-update-*")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(stageRoot)
	worktree := filepath.Join(stageRoot, "source")
	stagePrefix := filepath.Join(stageRoot, "prefix")
	if _, err := gitOutput(ctx, receipt.SourceDir, "worktree", "add", "--detach", worktree, result.CandidateCommit); err != nil {
		return Result{}, err
	}
	defer func() {
		_, _ = gitOutput(context.Background(), receipt.SourceDir, "worktree", "remove", "--force", worktree)
	}()
	if err := prepareBuildSubmodules(ctx, worktree); err != nil {
		return Result{}, fmt.Errorf("prepare update candidate submodules: %w", err)
	}

	installer := exec.CommandContext(ctx, "bash", filepath.Join(worktree, "scripts", "install.sh"))
	installer.Dir = worktree
	installer.Env = append(os.Environ(), "PREFIX="+stagePrefix)
	installer.Stdout = m.opts.Stderr
	installer.Stderr = m.opts.Stderr
	if err := installer.Run(); err != nil {
		return Result{}, fmt.Errorf("build update candidate %s: %w", result.CandidateCommit, err)
	}
	candidate := filepath.Join(stagePrefix, "bin", "mct-agent")
	versionOut, err := exec.CommandContext(ctx, candidate, "--version").CombinedOutput()
	if err != nil {
		return Result{}, fmt.Errorf("validate update candidate: %w: %s", err, strings.TrimSpace(string(versionOut)))
	}
	if !strings.Contains(string(versionOut), "commit: "+result.CandidateCommit) {
		return Result{}, fmt.Errorf("candidate commit verification failed: expected %s, output %q", result.CandidateCommit, strings.TrimSpace(string(versionOut)))
	}

	backup := receipt.BinaryPath + ".previous"
	if err := copyFile(receipt.BinaryPath, backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Result{}, fmt.Errorf("back up installed binary: %w", err)
	}
	sourceAdvanced := false
	activated := false
	defer func() {
		if retErr == nil {
			return
		}
		if activated {
			if _, err := os.Stat(backup); err == nil {
				if err := copyFile(backup, receipt.BinaryPath); err != nil {
					fmt.Fprintln(m.opts.Stderr, "Warning: unable to restore previous binary:", err)
				}
			}
		}
		if sourceAdvanced {
			if err := restoreSource(context.Background(), receipt.SourceDir, sourceHead, sourceBranch); err != nil {
				fmt.Fprintln(m.opts.Stderr, "Warning: unable to restore managed source checkout:", err)
			}
		}
	}()
	if _, err := gitOutput(ctx, receipt.SourceDir, "checkout", "-B", result.DefaultBranch, result.CandidateCommit); err != nil {
		return Result{}, err
	}
	sourceAdvanced = true
	if err := prepareBuildSubmodules(ctx, receipt.SourceDir); err != nil {
		return Result{}, fmt.Errorf("prepare managed source submodules: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(receipt.BinaryPath), 0o755); err != nil {
		return Result{}, err
	}
	if err := os.Rename(candidate, receipt.BinaryPath); err != nil {
		return Result{}, fmt.Errorf("activate update: %w", err)
	}
	activated = true

	receipt.DefaultBranch = result.DefaultBranch
	receipt.InstalledCommit = result.CandidateCommit
	receipt.InstalledVersion = versionFromOutput(string(versionOut))
	receipt.InstalledAt = m.opts.Now().UTC()
	if err := SaveReceipt(m.paths.Receipt, receipt); err != nil {
		return Result{}, err
	}
	activated = false
	sourceAdvanced = false
	result.Status = StatusUpdated
	result.CurrentCommit = result.CandidateCommit
	result.CheckedAt = m.opts.Now().UTC()
	_ = saveState(m.paths.State, State{LastAttemptAt: result.CheckedAt, LastSuccessAt: result.CheckedAt, LastSeenCommit: result.CandidateCommit})
	return result, nil
}

// prepareBuildSubmodules populates only the submodule needed to compile the
// primary mct-agent binary. The repository also contains large fixture and
// development submodules that managed updates should not download.
func prepareBuildSubmodules(ctx context.Context, source string) error {
	entry, err := gitOutput(ctx, source, "ls-tree", "HEAD", "--", buildSubmodulePath)
	if err != nil {
		return err
	}
	fields := strings.Fields(entry)
	if len(fields) == 0 || fields[0] != "160000" {
		return nil
	}
	if _, err := gitOutput(ctx, source, "submodule", "sync", "--", buildSubmodulePath); err != nil {
		return err
	}
	if _, err := gitOutput(ctx, source, "submodule", "update", "--init", "--recursive", "--depth=1", "--", buildSubmodulePath); err != nil {
		return err
	}
	return nil
}

func restoreSource(ctx context.Context, source, commit, branch string) error {
	args := []string{"checkout", "--detach", commit}
	if branch != "" {
		args = []string{"checkout", "-B", branch, commit}
	}
	if _, err := gitOutput(ctx, source, args...); err != nil {
		return err
	}
	return prepareBuildSubmodules(ctx, source)
}

func (m *Manager) ensureSource(ctx context.Context, receipt Receipt) error {
	if info, err := os.Stat(receipt.SourceDir); err == nil {
		if !info.IsDir() {
			return fmt.Errorf("managed source %s is not a directory", receipt.SourceDir)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(receipt.SourceDir), 0o700); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--single-branch", receipt.Remote, receipt.SourceDir)
	cmd.Stdout, cmd.Stderr = m.opts.Stderr, m.opts.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("re-create managed source: %w", err)
	}
	return nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".mct-agent-backup-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, destination)
}

func versionFromOutput(output string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(output), "\n")
	return strings.TrimSpace(strings.TrimPrefix(first, "mct-agent "))
}

func (m *Manager) Config() (Config, error) { return LoadConfig(m.paths.Config) }

func (m *Manager) Due(now time.Time) (bool, error) {
	cfg, err := LoadConfig(m.paths.Config)
	if err != nil {
		return false, err
	}
	if cfg.Policy == PolicyOff {
		return false, nil
	}
	state, err := loadState(m.paths.State)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !state.LastSuccessAt.IsZero() {
		return now.Sub(state.LastSuccessAt) >= cfg.Cooldown, nil
	}
	return now.Sub(state.LastAttemptAt) >= cfg.FailureRetry, nil
}

type fileLock struct{ file *os.File }

func (m *Manager) TryLock() (*fileLock, error) {
	if err := os.MkdirAll(m.paths.Root, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(m.paths.Lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	}
	return &fileLock{file: f}, nil
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}

func loadState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	return state, nil
}

func saveState(path string, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(data, '\n'), 0o600)
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-write-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
