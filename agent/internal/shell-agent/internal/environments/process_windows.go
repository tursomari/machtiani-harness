//go:build windows

package environments

import (
	"errors"
	"os"
	"os/exec"
)

func configureProcessGroup(_ *exec.Cmd) {}

func processGroupID(cmd *exec.Cmd) int { return cmd.Process.Pid }

func killProcessGroup(cmd *exec.Cmd, _ int) error {
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
