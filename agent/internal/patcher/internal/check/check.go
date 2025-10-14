package check

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// ApplyCheck runs `git apply --check --unsafe-paths` to verify the patch applies cleanly.
func ApplyCheck(repoRoot, patchPath string) error {
	cmd := exec.Command("git", "apply", "--check", "--unsafe-paths", patchPath)
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(strings.TrimSpace(stderr.String()))
	}
	return nil
}
