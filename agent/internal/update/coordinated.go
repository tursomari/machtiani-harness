package update

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// The public DearMachine launcher identifies the owning installation, including
// a custom data directory. Standalone receipts never override this owner.
func (m *Manager) CoordinatedLauncher() (string, error) {
	launcher := filepath.Join(m.opts.Home, ".local", "bin", "dearmachine")
	target, err := os.Readlink(launcher)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		if info, statErr := os.Lstat(launcher); statErr == nil && info.Mode()&os.ModeSymlink == 0 {
			return "", nil
		}
		return "", err
	}
	current := filepath.Dir(filepath.Dir(target))
	if !filepath.IsAbs(target) || filepath.Base(current) != "current" || filepath.Base(filepath.Dir(current)) != "dearmachine" || target != filepath.Join(current, "bin", "dearmachine") {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(current, "release.json"))
	if err != nil {
		return "", err
	}
	var receipt struct {
		Version int    `json:"version"`
		Method  string `json:"method"`
	}
	if err = json.Unmarshal(data, &receipt); err != nil {
		return "", err
	}
	if receipt.Version != 1 || receipt.Method != "nix" {
		return "", errors.New("unsupported coordinated installation receipt")
	}
	info, err := os.Stat(launcher)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", errors.New("coordinated launcher is not executable")
	}
	return launcher, nil
}

func (m *Manager) requireStandalone() error {
	launcher, err := m.CoordinatedLauncher()
	if err != nil {
		return err
	}
	if launcher != "" {
		return errors.New("Machtiani is managed by DearMachine; use dearmachine update")
	}
	return nil
}
