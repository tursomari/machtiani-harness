// Package modes owns the canonical mode catalogue installed in a user's home.
package modes

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

//go:embed canonical
var canonical embed.FS

// Names returns the canonical mode names in stable order.
func Names() ([]string, error) {
	entries, err := fs.ReadDir(canonical, "canonical")
	if err != nil {
		return nil, fmt.Errorf("read embedded modes: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// SyncCanonical mirrors managed canonical modes into ~/.machtiani/modes.
// Directories whose names are not in the embedded catalogue are untouched.
func SyncCanonical() error {
	root, err := projectstore.ModesRoot()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create modes directory %s: %w", root, err)
	}
	names, err := Names()
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := syncMode(root, name); err != nil {
			return err
		}
	}
	return nil
}

func syncMode(root, name string) error {
	tmp, err := os.MkdirTemp(root, ".mode-"+name+"-*")
	if err != nil {
		return fmt.Errorf("stage canonical mode %s: %w", name, err)
	}
	defer os.RemoveAll(tmp)
	sourceRoot := filepath.ToSlash(filepath.Join("canonical", name))
	if err := fs.WalkDir(canonical, sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(canonical, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	}); err != nil {
		return fmt.Errorf("stage canonical mode %s: %w", name, err)
	}

	destination := filepath.Join(root, name)
	backup := destination + ".previous"
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove stale mode backup %s: %w", backup, err)
	}
	if _, err := os.Stat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return fmt.Errorf("backup canonical mode %s: %w", name, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect canonical mode %s: %w", name, err)
	}
	if err := os.Rename(tmp, destination); err != nil {
		_ = os.Rename(backup, destination)
		return fmt.Errorf("activate canonical mode %s: %w", name, err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove canonical mode backup %s: %w", name, err)
	}
	return nil
}
