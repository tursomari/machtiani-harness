package mct

import (
    dr "github.com/tursomari/machtiani/agent/internal/mct/internal/discoveryrunner"
)

// RefreshSyncedWorkspace exposes discovery workspace refresh to callers outside
// the mct/internal subtree, while keeping the implementation in an internal
// package.
func RefreshSyncedWorkspace(sessionID string, changed []string, verbose bool) error {
    return dr.RefreshSyncedWorkspace(sessionID, changed, verbose)
}

