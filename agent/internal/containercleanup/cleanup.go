package containercleanup

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"strings"
)

const (
	defaultCleanupImage = "busybox:1.36"
	cleanupMountPoint   = "/mnt/machtiani-cleanup"
)

// Options controls how container cleanup is performed.
type Options struct {
	Runtime        string
	DockerfilePath string
	Image          string
}

// RemoveAll attempts to remove the target path using a container runtime.
func RemoveAll(targetPath string, opts Options) error {
	trimmed := strings.TrimSpace(targetPath)
	if trimmed == "" {
		return fmt.Errorf("cleanup path is empty")
	}

	absTarget, err := filepath.Abs(trimmed)
	if err != nil {
		return fmt.Errorf("resolve cleanup path %s: %w", trimmed, err)
	}
	if absTarget == string(os.PathSeparator) {
		return fmt.Errorf("refusing to remove root path")
	}

	runtimePath, err := resolveRuntime(opts.Runtime)
	if err != nil {
		return err
	}

	image, err := selectImage(runtimePath, opts)
	if err != nil {
		return err
	}

	parent := filepath.Dir(absTarget)
	rel, err := filepath.Rel(parent, absTarget)
	if err != nil {
		return fmt.Errorf("resolve cleanup path %s: %w", absTarget, err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("cleanup path %s is not within parent %s", absTarget, parent)
	}

	containerTarget := path.Join(cleanupMountPoint, filepath.ToSlash(rel))
	volume := fmt.Sprintf("%s:%s:rw", parent, cleanupMountPoint)

	args := []string{
		"run",
		"--rm",
		"--network=none",
		"--volume", volume,
		image,
		"rm",
		"-rf",
		"--",
		containerTarget,
	}

	cmd := exec.Command(runtimePath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmedOutput := strings.TrimSpace(string(output))
		if trimmedOutput == "" {
			trimmedOutput = err.Error()
		}
		runtimeName := filepath.Base(runtimePath)
		return fmt.Errorf("cleanup container with %s: %s", runtimeName, trimmedOutput)
	}

	return nil
}

func selectImage(runtimePath string, opts Options) (string, error) {
	if image := strings.TrimSpace(opts.Image); image != "" {
		return image, nil
	}

	dockerfile := strings.TrimSpace(opts.DockerfilePath)
	if dockerfile != "" {
		tag, err := computeImageTag(dockerfile)
		if err == nil {
			exists, existsErr := imageExists(runtimePath, tag)
			if existsErr != nil {
				return "", existsErr
			}
			if exists {
				return tag, nil
			}
		}
	}

	return defaultCleanupImage, nil
}

func resolveRuntime(preferred string) (string, error) {
	preferred = strings.TrimSpace(preferred)
	if preferred != "" {
		path, err := exec.LookPath(preferred)
		if err != nil {
			return "", fmt.Errorf("container runtime %q not found: %w", preferred, err)
		}
		return path, nil
	}

	defaultCandidates := []string{"docker", "podman"}
	var tried []string
	for _, candidate := range defaultCandidates {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, nil
		}
		tried = append(tried, candidate)
	}

	return "", fmt.Errorf("no container runtime found (tried %s)", strings.Join(tried, ", "))
}

func computeImageTag(dockerfilePath string) (string, error) {
	absPath, err := normalizeDockerfilePath(dockerfilePath)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		return "", fmt.Errorf("read dockerfile %s: %w", absPath, err)
	}

	h := sha256.New()
	h.Write([]byte(absPath))
	h.Write([]byte{'\n'})
	h.Write(data)
	sum := h.Sum(nil)

	prefix := "shell-agent-"
	return prefix + hex.EncodeToString(sum)[:12], nil
}

func normalizeDockerfilePath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("dockerfile path must not be empty")
	}

	expanded, err := expandUser(path)
	if err != nil {
		return "", err
	}

	absPath, err := filepath.Abs(expanded)
	if err != nil {
		return "", fmt.Errorf("resolve dockerfile path: %w", err)
	}
	return absPath, nil
}

func imageExists(runtimePath, tag string) (bool, error) {
	inspectCmd := exec.Command(runtimePath, "image", "inspect", tag)
	output, err := inspectCmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false, fmt.Errorf("container runtime %q not found in PATH: %w", runtimePath, err)
	}

	lowerOutput := strings.ToLower(string(output))
	missing := strings.Contains(lowerOutput, "no such object") ||
		strings.Contains(lowerOutput, "no such image") ||
		strings.Contains(lowerOutput, "not found")

	if exitErr, ok := err.(*exec.ExitError); ok {
		if missing {
			return false, nil
		}
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			trimmed = exitErr.Error()
		}
		runtimeName := filepath.Base(runtimePath)
		return false, fmt.Errorf("inspect image %s with %s: %s", tag, runtimeName, trimmed)
	}

	return false, fmt.Errorf("inspect image %s: %w", tag, err)
}

func expandUser(path string) (string, error) {
	if path == "" || path[0] != '~' {
		return path, nil
	}

	if len(path) == 1 || path[1] == '/' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve current user home: %w", err)
		}
		return filepath.Join(home, strings.TrimPrefix(path[1:], "/")), nil
	}

	sep := strings.IndexByte(path, '/')
	var userPart string
	var remainder string
	if sep == -1 {
		userPart = path[1:]
		remainder = ""
	} else {
		userPart = path[1:sep]
		remainder = path[sep:]
	}

	u, err := user.Lookup(userPart)
	if err != nil {
		return "", fmt.Errorf("resolve home for user %q: %w", userPart, err)
	}
	if remainder == "" {
		return u.HomeDir, nil
	}
	return filepath.Join(u.HomeDir, strings.TrimPrefix(remainder, "/")), nil
}
