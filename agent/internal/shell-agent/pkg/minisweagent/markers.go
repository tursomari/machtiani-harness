package minisweagent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

const FinalMarkerFilename = "mct-swe-agent-finale.txt"

// FinalMarkerPath returns the path to the final marker file.
// If sessionID is provided (non-empty), it will be appended to the filename for session
// scoping, preventing cross-session context pollution.
// If sessionID is empty, this function behaves as before for backward compatibility.
//
// The marker file is resolved as follows:
// 1. If MINISWE_FINAL_DIR environment variable is set, use it as the directory
// 2. Otherwise, use os.TempDir() as the directory
// 3. The filename is constructed as:
//    - With sessionID: "mct-swe-agent-finale-<sessionID>.txt"
//    - Without sessionID: "mct-swe-agent-finale.txt"
//
// IMPORTANT: For multi-session or multi-agent deployments, always pass a unique
// sessionID to ensure isolation. This prevents marker files from one session from
// being read by another, causing context pollution and non-deterministic behavior.
func FinalMarkerPath(sessionID ...string) string {
	dir := os.Getenv("MINISWE_FINAL_DIR")
	if dir == "" {
		dir = os.TempDir()
	}

	filename := FinalMarkerFilename
	if len(sessionID) > 0 && sessionID[0] != "" {
		// Insert session ID before the file extension
		const ext = ".txt"
		base := FinalMarkerFilename[:len(FinalMarkerFilename)-len(ext)]
		filename = fmt.Sprintf("%s-%s%s", base, sessionID[0], ext)
	}

	return filepath.Join(dir, filename)
}

// GenerateSessionID creates a new unique session ID using UUID v4.
// This ID should be incorporated into marker file paths to ensure per-session isolation.
func GenerateSessionID() string {
	return uuid.New().String()
}
