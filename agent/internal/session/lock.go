package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// sessionLock maintains an exclusive lock file for the lifetime of a session.
type sessionLock struct {
	file      *os.File
	path      string
	sessionID string
}

func acquireSessionLock(sessionID, sessionRoot string) (*sessionLock, error) {
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		return nil, fmt.Errorf("prepare session lock root %s: %w", sessionRoot, err)
	}
	lockPath := filepath.Join(sessionRoot, sessionLockFileName)
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open session lock %s: %w", lockPath, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("session already active for %s", lockPath)
		}
		return nil, fmt.Errorf("lock session file %s: %w", lockPath, err)
	}

	if err := initialiseLockFile(file, sessionID); err != nil {
		syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
		return nil, err
	}

	lock := &sessionLock{
		file:      file,
		path:      lockPath,
		sessionID: sessionID,
	}

	return lock, nil
}

func initialiseLockFile(file *os.File, sessionID string) error {
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("seek session lock: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate session lock: %w", err)
	}
	info := fmt.Sprintf("session_id=%s\npid=%d\n", sessionID, os.Getpid())
	if _, err := file.WriteString(info); err != nil {
		return fmt.Errorf("write session lock: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync session lock: %w", err)
	}
	now := time.Now()
	if err := os.Chtimes(file.Name(), now, now); err != nil {
		return fmt.Errorf("touch session lock: %w", err)
	}
	return nil
}

func (l *sessionLock) Close() error {
	if l == nil {
		return nil
	}

	var errs []error
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		errs = append(errs, fmt.Errorf("unlock session file %s: %w", l.path, err))
	}
	if err := l.file.Close(); err != nil {
		errs = append(errs, fmt.Errorf("close session file %s: %w", l.path, err))
	}
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove session file %s: %w", l.path, err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
