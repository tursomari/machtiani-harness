//go:build !windows

package configfiles

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func openPrivate(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open credential file %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	okay := false
	defer func() {
		if !okay {
			f.Close()
		}
	}()
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
	okay = true
	return f, nil
}
func protectPrivate(path string) error { return os.Chmod(path, 0600) }
