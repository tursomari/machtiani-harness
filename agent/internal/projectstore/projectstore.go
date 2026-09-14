// Package projectstore resolves Machtiani project identity and per-user state.
package projectstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/tursomari/machtiani/agent/internal/configfiles"
	"github.com/tursomari/machtiani/agent/internal/git"
)

const (
	RootDirName      = ".machtiani"
	MarkerFileName   = "project.uuid"
	MetadataName     = "project.toml"
	GlobalConfig     = "config.toml"
	ModesDirName     = "modes"
	SessionsDirName  = "sessions"
	ArtifactsDirName = "artifacts"
	ScratchDirName   = "tmp"
	MetaDirName      = "meta-orchestrator"
)

// Status describes whether a project is ready for the UUID home-store layout.
type Status string

const (
	StatusUninitialized Status = "uninitialized"
	StatusLegacy        Status = "legacy"
	StatusInitialized   Status = "initialized"
)

// ConfigScope selects the complete configuration used by a project.
type ConfigScope string

const (
	ScopeGlobal  ConfigScope = "global"
	ScopeProject ConfigScope = "project"
)

// Context contains the resolved paths for one project.
type Context struct {
	ProjectRoot string
	HomeRoot    string
	StoreRoot   string
	MarkerPath  string
	ID          uuid.UUID
	Status      Status
	ConfigScope ConfigScope
}

// Discover resolves project identity without creating or modifying anything.
func Discover(start string) (Context, error) {
	root, err := findProjectRoot(start)
	if err != nil {
		return Context{}, err
	}
	home, err := HomeRoot()
	if err != nil {
		return Context{}, err
	}
	ctx := Context{
		ProjectRoot: root,
		HomeRoot:    home,
		MarkerPath:  MarkerPath(root),
		Status:      StatusUninitialized,
		ConfigScope: ScopeGlobal,
	}
	id, ok, err := ReadProjectUUID(root)
	if err != nil {
		return Context{}, err
	}
	if ok {
		ctx.ID = id
		ctx.StoreRoot = filepath.Join(home, id.String())
		ctx.Status = StatusInitialized
		scope, err := ReadConfigScope(ctx.StoreRoot)
		if err != nil {
			return Context{}, err
		}
		ctx.ConfigScope = scope
		return ctx, nil
	}
	legacy, err := HasLegacyState(root)
	if err != nil {
		return Context{}, err
	}
	if legacy {
		ctx.Status = StatusLegacy
	}
	return ctx, nil
}

// HomeRoot returns the per-user Machtiani root.
func HomeRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("resolve user home: empty path")
	}
	return filepath.Join(home, RootDirName), nil
}

// GlobalConfigPath returns the native configuration beneath XDG_CONFIG_HOME.
func GlobalConfigPath() (string, error) {
	root, err := configfiles.Root()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "machtiani", GlobalConfig)
	return path, nil
}

// ModesRoot returns ~/.machtiani/modes.
func ModesRoot() (string, error) {
	root, err := HomeRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ModesDirName), nil
}

// MarkerPath returns the repository marker path.
func MarkerPath(projectRoot string) string {
	return filepath.Join(projectRoot, RootDirName, MarkerFileName)
}

// ReadProjectUUID reads and validates a project marker.
func ReadProjectUUID(projectRoot string) (uuid.UUID, bool, error) {
	path := MarkerPath(projectRoot)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("read project marker %s: %w", path, err)
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" || strings.ContainsAny(raw, "\r\n\t ") {
		return uuid.Nil, false, fmt.Errorf("invalid project UUID in %s", path)
	}
	id, err := uuid.Parse(raw)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false, fmt.Errorf("invalid project UUID in %s", path)
	}
	return id, true, nil
}

// WriteProjectUUID atomically writes a trackable project marker.
func WriteProjectUUID(projectRoot string, id uuid.UUID) error {
	if id == uuid.Nil {
		return errors.New("project UUID is required")
	}
	return atomicWrite(MarkerPath(projectRoot), []byte(id.String()+"\n"), 0o644)
}

// HasLegacyState reports whether .machtiani contains anything except a marker.
func HasLegacyState(projectRoot string) (bool, error) {
	dir := filepath.Join(projectRoot, RootDirName)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read legacy state %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.Name() == MarkerFileName {
			continue
		}
		return true, nil
	}
	return false, nil
}

// EnsureLayout creates private per-project state directories.
func EnsureLayout(storeRoot string) error {
	for _, path := range []string{
		storeRoot,
		filepath.Join(storeRoot, SessionsDirName),
		filepath.Join(storeRoot, ArtifactsDirName),
		filepath.Join(storeRoot, ScratchDirName),
		filepath.Join(storeRoot, MetaDirName),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create project store %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("secure project store %s: %w", path, err)
		}
	}
	return nil
}

// ReadConfigScope reads project metadata. Missing metadata defaults to global.
func ReadConfigScope(storeRoot string) (ConfigScope, error) {
	path := filepath.Join(storeRoot, MetadataName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ScopeGlobal, nil
	}
	if err != nil {
		return "", fmt.Errorf("read project metadata %s: %w", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "config_scope" {
			continue
		}
		scope := ConfigScope(strings.Trim(strings.TrimSpace(value), `"`))
		if err := scope.Validate(); err != nil {
			return "", fmt.Errorf("read project metadata %s: %w", path, err)
		}
		return scope, nil
	}
	return "", fmt.Errorf("read project metadata %s: config_scope is required", path)
}

// WriteConfigScope atomically stores the selected complete config scope.
func WriteConfigScope(storeRoot string, scope ConfigScope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	data := []byte(fmt.Sprintf("version = 1\nconfig_scope = %q\n", scope))
	return atomicWrite(filepath.Join(storeRoot, MetadataName), data, 0o600)
}

// Validate checks that the scope is supported.
func (s ConfigScope) Validate() error {
	if s != ScopeGlobal && s != ScopeProject {
		return fmt.Errorf("invalid config scope %q", s)
	}
	return nil
}

// ProjectConfigPath returns the complete UUID-scoped config path.
func (c Context) ProjectConfigPath() string { return filepath.Join(c.StoreRoot, GlobalConfig) }
func (c Context) SessionsRoot() string      { return filepath.Join(c.StoreRoot, SessionsDirName) }
func (c Context) ArtifactsRoot() string     { return filepath.Join(c.StoreRoot, ArtifactsDirName) }
func (c Context) ScratchRoot() string       { return filepath.Join(c.StoreRoot, ScratchDirName) }
func (c Context) MetaRoot() string          { return filepath.Join(c.StoreRoot, MetaDirName) }

func findProjectRoot(start string) (string, error) {
	if strings.TrimSpace(start) == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
	}
	abs, err := filepath.Abs(start)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	if git.IsGitRepo(abs) {
		root, err := git.RepoRoot(abs)
		if err != nil {
			return "", fmt.Errorf("resolve git root: %w", err)
		}
		return filepath.Clean(root), nil
	}
	for dir := filepath.Clean(abs); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(MarkerPath(dir)); err == nil {
			return dir, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect project marker %s: %w", MarkerPath(dir), err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return filepath.Clean(abs), nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".machtiani-write-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("set permissions for %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
