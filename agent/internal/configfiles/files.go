// Package configfiles owns private configuration and credential file storage.
package configfiles

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"

	"github.com/BurntSushi/toml"
)

func Root() (string, error) {
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("XDG_CONFIG_HOME must be absolute")
		}
		return root, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}

func Resolve(config, reference string) string {
	if filepath.IsAbs(reference) {
		return reference
	}
	path := filepath.Join(filepath.Dir(config), reference)
	if absolute, err := filepath.Abs(path); err == nil {
		return absolute
	}
	return path
}

func Read(path string) (map[string]string, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open credential file %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1048576 {
		return nil, fmt.Errorf("credential file %s must be a private regular file", path)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Getuid()) {
		return nil, errors.New("credential file must be owned by current user")
	}
	var data bytes.Buffer
	if _, err = data.ReadFrom(f); err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(data.String(), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || !ValidName(name) || value == "" || strings.ContainsFunc(value, unicode.IsSpace) || strings.ContainsAny(value, "\x00") {
			return nil, errors.New("invalid credential file assignment")
		}
		if _, exists := values[name]; exists {
			return nil, errors.New("duplicate credential file assignment")
		}
		values[name] = value
	}
	return values, nil
}

func ValidName(name string) bool {
	if name == "" {
		return false
	}
	for i, c := range name {
		if !(c == '_' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func Write(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("destination must be a regular file, not a symlink")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("destination directory must not be a symlink")
	}
	f, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Save removes literal provider keys from TOML and stores them in a sibling
// private file. Existing environment references remain explicit references.
func Save(path string, raw map[string]any) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("configuration destination must be a regular file, not a symlink")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	reference, _ := raw["credentials_file"].(string)
	if reference == "" {
		reference = "credentials.env"
	}
	credentials := Resolve(path, reference)
	values := map[string]string{}
	dirty := false
	if providers, ok := raw["providers"].(map[string]any); ok {
		for name, entry := range providers {
			p, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			key, _ := p["api_key"].(string)
			if key == "" || strings.HasPrefix(key, "${") {
				continue
			}
			if strings.ContainsFunc(key, unicode.IsSpace) || strings.ContainsAny(key, "\x00") {
				return errors.New("API key must be one nonempty line without whitespace")
			}
			if !dirty {
				existing, err := Read(credentials)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if existing != nil {
					values = existing
				}
			}
			digest := sha256.Sum256([]byte(name))
			variable := fmt.Sprintf("MACHTIANI_PROVIDER_%X_API_KEY", digest[:12])
			values[variable] = key
			p["api_key_ref"] = variable
			delete(p, "api_key")
			dirty = true
		}
	}
	if dirty {
		names := make([]string, 0, len(values))
		for name := range values {
			names = append(names, name)
		}
		sort.Strings(names)
		var data strings.Builder
		for _, name := range names {
			fmt.Fprintf(&data, "%s=%s\n", name, values[name])
		}
		if err := Write(credentials, []byte(data.String())); err != nil {
			return err
		}
		raw["credentials_file"] = reference
	}
	var data bytes.Buffer
	if err := toml.NewEncoder(&data).Encode(raw); err != nil {
		return err
	}
	return Write(path, data.Bytes())
}

// MigrateGlobal copies the legacy configuration only when the new path is absent.
// The legacy file is retained for recovery; existing destinations always win.
func MigrateGlobal(target string) error {
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	old := filepath.Join(home, ".machtiani", "config.toml")
	if _, err := os.Stat(old); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return Import(old, target, "")
}

// Import copies a configuration without overwriting an existing destination.
// Only credentials referenced by that configuration are copied from the source.
func Import(source, target, environmentFile string) error {
	if _, err := os.Lstat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return errors.New("cannot import invalid configuration")
	}
	sourceReference, _ := raw["credentials_file"].(string)
	if sourceReference != "" {
		environmentFile = Resolve(source, sourceReference)
	}
	values := map[string]string{}
	if environmentFile != "" {
		values, err = Read(environmentFile)
		if err != nil {
			return err
		}
	}
	selected := map[string]string{}
	if providers, ok := raw["providers"].(map[string]any); ok {
		for _, entry := range providers {
			p, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			key, _ := p["api_key"].(string)
			if ref, ok := p["api_key_ref"].(string); ok && ref != "" {
				key = "${" + ref + "}"
			}
			if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
				name := key[2 : len(key)-1]
				if value, ok := values[name]; ok {
					selected[name] = value
					p["api_key_ref"] = name
					delete(p, "api_key")
				}
			}
			if profile, ok := p["profile"].(string); ok && profile != "" && !strings.HasPrefix(profile, "~/") && !filepath.IsAbs(profile) {
				p["profile"] = Resolve(source, profile)
			}
		}
	}
	rebaseTemplateFiles(raw, source)
	delete(raw, "credentials_file")
	if len(selected) > 0 {
		var data strings.Builder
		names := make([]string, 0, len(selected))
		for name := range selected {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(&data, "%s=%s\n", name, selected[name])
		}
		destination := filepath.Join(filepath.Dir(target), "credentials.env")
		if existing, err := Read(destination); err == nil {
			for name, value := range selected {
				if existing[name] != value {
					return errors.New("credential destination conflicts with imported credentials")
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else if err := Write(destination, []byte(data.String())); err != nil {
			return err
		}
		raw["credentials_file"] = "credentials.env"
	} else if sourceReference != "" {
		raw["credentials_file"] = Resolve(source, sourceReference)
	}
	return Save(target, raw)
}

func rebaseTemplateFiles(table map[string]any, source string) {
	for name, value := range table {
		child, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if strings.HasSuffix(name, "_template") || name == "template" {
			if file, ok := child["file"].(string); ok && file != "" && !strings.HasPrefix(file, "~/") && !filepath.IsAbs(file) {
				child["file"] = Resolve(source, file)
			}
		}
		rebaseTemplateFiles(child, source)
	}
}
