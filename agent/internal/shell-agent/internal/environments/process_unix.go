//go:build unix

package environments

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func processGroupID(cmd *exec.Cmd) int {
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Pid
	}
	return pgid
}

func killProcessGroup(cmd *exec.Cmd, pgid int) error {
	if pgid > 0 {
		if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
			return nil
		} else {
			return err
		}
	}
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
