package session

import (
	"fmt"
	"os"
	"path/filepath"
)

func writeConversationFileAtomic(path string, data []byte) (retErr error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".conversation-*")
	if err != nil {
		return fmt.Errorf("create temporary conversation file: %w", err)
	}
	tmpPath := tmp.Name()
	closed := false
	defer func() {
		if retErr != nil {
			if !closed {
				_ = tmp.Close()
			}
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("set temporary conversation file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary conversation file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary conversation file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		closed = true
		return fmt.Errorf("close temporary conversation file: %w", err)
	}
	closed = true
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace conversation file: %w", err)
	}
	return nil
}
