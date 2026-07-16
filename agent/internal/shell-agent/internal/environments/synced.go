package environments

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// SyncedEnvironment wraps an Environment and tracks workspace sync progress.
// It implements the Environment interface while delegating to the underlying
// environment and maintaining sync progress metrics.
type SyncedEnvironment struct {
	underlying    minisweagent.Environment
	syncProgress  atomic.Value // *float64
	syncStatus    atomic.Value // string
	syncMutex     sync.Mutex
}

// NewSyncedEnvironment wraps an environment to track sync progress.
func NewSyncedEnvironment(env minisweagent.Environment) *SyncedEnvironment {
	return &SyncedEnvironment{
		underlying:   env,
		syncProgress: atomic.Value{},
		syncStatus:   atomic.Value{},
	}
}

// Config delegates to the underlying environment.
func (e *SyncedEnvironment) Config() interface{} {
	return e.underlying.Config()
}

// Execute delegates to the underlying environment.
func (e *SyncedEnvironment) Execute(ctx context.Context, command, cwd string) (minisweagent.ExecuteResult, error) {
	return e.underlying.Execute(ctx, command, cwd)
}

// GetTemplateVars delegates to the underlying environment.
func (e *SyncedEnvironment) GetTemplateVars() map[string]interface{} {
	return e.underlying.GetTemplateVars()
}

// GetSyncProgress returns the current workspace sync progress (0.0 to 1.0).
func (e *SyncedEnvironment) GetSyncProgress() float64 {
	// Try to get from atomic value first
	if val := e.syncProgress.Load(); val != nil {
		if progress, ok := val.(*float64); ok && progress != nil {
			return *progress
		}
	}

	// Fall back to underlying environment
	return e.underlying.GetSyncProgress()
}

// GetSyncStatus returns the current workspace sync status description.
func (e *SyncedEnvironment) GetSyncStatus() string {
	// Try to get from atomic value first
	if val := e.syncStatus.Load(); val != nil {
		if status, ok := val.(string); ok {
			return status
		}
	}

	// Fall back to underlying environment
	return e.underlying.GetSyncStatus()
}

// SetSyncProgress updates the workspace sync progress (0.0 to 1.0).
// Thread-safe.
func (e *SyncedEnvironment) SetSyncProgress(progress float64) {
	if progress < 0.0 {
		progress = 0.0
	} else if progress > 1.0 {
		progress = 1.0
	}
	e.syncProgress.Store(&progress)
}

// SetSyncStatus updates the workspace sync status description.
// Thread-safe.
func (e *SyncedEnvironment) SetSyncStatus(status string) {
	e.syncStatus.Store(status)
}
