package session

// Package session manages agent run lifecycle, state persistence, session
// locking, transcript generation, and turn processing.
//
// Locking contract: session directories are protected by an advisory file lock
// (flock) on the session.lock file within each session scratch directory.
// Acquisition uses LOCK_EX | LOCK_NB with 3 attempts and exponential backoff
// (100ms, then 200ms). The lock file content is "session_id=<id>\npid=<pid>\n".
// A lock file whose recorded PID is dead is considered stale and may be
// pruned by cleanup logic (see cleanup.go). cleanupOrphanedSessionDirs
// preserves active sessions (those holding an exclusive flock on the lock
// file) and removes orphaned ones.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
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

	var lockErr error
	for attempt := 0; attempt < 3; attempt++ {
		lockErr = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			break
		}
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
			file.Close()
			return nil, fmt.Errorf("lock session file %s: %w", lockPath, lockErr)
		}
		if attempt < 2 {
			time.Sleep(time.Duration(100*(1<<attempt)) * time.Millisecond)
		}
	}
	if lockErr != nil {
		file.Close()
		return nil, fmt.Errorf("session already active for %s", lockPath)
	}

	// Validate that any prior PID recorded in the lock file is not still
	// alive. This guards against PID reuse races where the kernel released
	// a stale flock but the recorded PID now belongs to a different process.
	if err := validateLockPID(file); err != nil {
		syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		file.Close()
		return nil, fmt.Errorf("session lock PID validation failed for %s: %w", lockPath, err)
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

// validateLockPID checks that the PID recorded in the lock file (if any)
// does not belong to a running process other than ourselves. If the lock
// file does not exist, the recorded PID is malformed, or the PID is dead,
// it returns nil. If the PID belongs to a running process other than
// os.Getpid(), it returns an error.
func validateLockPID(file *os.File) error {
	data, err := os.ReadFile(file.Name())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read lock file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if !strings.HasPrefix(line, "pid=") {
			continue
		}
		pidStr := strings.TrimPrefix(line, "pid=")
		pid, parseErr := strconv.Atoi(strings.TrimSpace(pidStr))
		if parseErr != nil {
			return nil
		}
		if pid == os.Getpid() {
			return nil
		}
		process, findErr := os.FindProcess(pid)
		if findErr != nil {
			return nil
		}
		if signalErr := process.Signal(syscall.Signal(0)); signalErr != nil {
			return nil
		}
		return fmt.Errorf("lock file PID %d is still alive (conflicting process)", pid)
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

// IsSessionActive checks whether the session identified by sessionID is
// currently locked by an active process.
func IsSessionActive(sessionID string) (bool, error) {
	scratchDir, err := artifacts.SessionScratchDirectory(sessionID)
	if err != nil {
		return false, fmt.Errorf("resolve scratch directory: %w", err)
	}
	lockPath := filepath.Join(scratchDir, sessionLockFileName)

	file, err := os.OpenFile(lockPath, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("open session lock %s: %w", lockPath, err)
	}
	defer file.Close()

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return true, nil
		}
		return false, fmt.Errorf("check session lock %s: %w", lockPath, err)
	}

	// Successfully acquired shared lock, release it.
	syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	return false, nil
}
