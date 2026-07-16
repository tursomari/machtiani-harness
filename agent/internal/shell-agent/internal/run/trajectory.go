package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

// ResumeStateVersion is the current schema version for resume state.
const ResumeStateVersion = 1

// DefaultMigrations holds the default sequence of resume state migrations.
var DefaultMigrations = map[string]func([]byte) ([]byte, error){}

// ErrIncompatibleResumeVersion is returned when a resume state version does not match the current version.
type ErrIncompatibleResumeVersion struct {
	Got  int
	Want int
}

func (e ErrIncompatibleResumeVersion) Error() string {
	return fmt.Sprintf("resume state version %d is incompatible with current version %d", e.Got, e.Want)
}

// AgentStateProvider is implemented by agents that can expose their resume state.
type AgentStateProvider interface {
	GetResumeState() *ResumeState
}

// AgentStateRestorer is implemented by agents that can restore from a resume state.
type AgentStateRestorer interface {
	RestoreResumeState(rs *ResumeState)
}

// ResumeState captures the mutable agent state needed to resume an interrupted run.
type ResumeState struct {
	Version                      int    `json:"version"`
	StepCounter                  int    `json:"step_counter"`
	CommandsExecuted             int    `json:"commands_executed"`
	LastNonEmptyOutput           string `json:"last_non_empty_output"`
	FinalizeRequested            bool   `json:"finalize_requested"`
	ConsecutiveFormatErrors      int    `json:"consecutive_format_errors"`
	ConsecutiveFinalizeReminders int    `json:"consecutive_finalize_reminders"`
	SystemPrompt                 string `json:"system_prompt"`
	SystemPromptCached           bool   `json:"system_prompt_cached"`
	AnswerTag                    string `json:"answer_tag,omitempty"`
	CommandTag                   string `json:"command_tag,omitempty"`
}

// ValidateVersion returns nil if the resume state version matches the current version.
func (rs *ResumeState) ValidateVersion() error {
	if rs.Version != ResumeStateVersion {
		return ErrIncompatibleResumeVersion{Got: rs.Version, Want: ResumeStateVersion}
	}
	return nil
}

// MigrateResumeState reads the stored version from data, applies any necessary
// migrations, and returns the migrated data. If the stored version equals
// currentVersion the data is returned unchanged.
func MigrateResumeState(data []byte, currentVersion int, migrations map[string]func([]byte) ([]byte, error)) ([]byte, error) {
	type versionOnly struct {
		Version int `json:"version"`
	}

	// Try nested resume_state first (FileTrajectory format).
	type nestedVersion struct {
		ResumeState *versionOnly `json:"resume_state,omitempty"`
	}
	var nv nestedVersion
	if err := json.Unmarshal(data, &nv); err != nil {
		return nil, fmt.Errorf("unmarshal resume state version: %w", err)
	}

	storedVersion := 0
	if nv.ResumeState != nil {
		storedVersion = nv.ResumeState.Version
	}

	// Fall back to top-level version (raw ResumeState format).
	if storedVersion == 0 {
		var tv versionOnly
		if err := json.Unmarshal(data, &tv); err != nil {
			return nil, fmt.Errorf("unmarshal version: %w", err)
		}
		storedVersion = tv.Version
	}

	if storedVersion == currentVersion {
		return data, nil
	}

	if storedVersion > currentVersion {
		return nil, ErrIncompatibleResumeVersion{Got: storedVersion, Want: currentVersion}
	}

	result := data
	for v := storedVersion; v < currentVersion; v++ {
		key := fmt.Sprintf("v%d_to_v%d", v, v+1)
		migrateFn, ok := migrations[key]
		if !ok {
			return nil, fmt.Errorf("no migration defined from version %d to %d", v, v+1)
		}
		var err error
		result, err = migrateFn(result)
		if err != nil {
			return nil, fmt.Errorf("migration %s failed: %w", key, err)
		}
	}

	return result, nil
}

// FileTrajectory augments the core trajectory with metadata used when persisting to disk.
type FileTrajectory struct {
	Messages    []minisweagent.Message  `json:"messages"`
	ExitStatus  string                  `json:"exit_status"`
	Result      string                  `json:"result"`
	ExtraInfo   map[string]interface{}  `json:"extra_info,omitempty"`
	Timestamp   time.Time               `json:"timestamp"`
	ResumeState *ResumeState            `json:"resume_state,omitempty"`
}

// SaveTrajectory persists a trajectory to a JSON file.
func SaveTrajectory(traj FileTrajectory, path string) error {
	data, err := MarshalTrajectory(traj)
	if err != nil {
		return fmt.Errorf("marshal trajectory: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save trajectory directory: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write trajectory: %w", err)
	}
	return nil
}

// MarshalTrajectory renders the trajectory into a stable, human-readable JSON payload.
func MarshalTrajectory(traj FileTrajectory) ([]byte, error) {
	return json.MarshalIndent(traj, "", "  ")
}

// FromAgent constructs a file trajectory using the provided agent state.
func FromAgent(agent minisweagent.Agent, exitStatus, result string, extra map[string]interface{}) FileTrajectory {
	traj := FileTrajectory{
		Messages:   agent.Messages(),
		ExitStatus: exitStatus,
		Result:     result,
		ExtraInfo:  extra,
		Timestamp:  time.Now().UTC(),
	}
	if provider, ok := agent.(AgentStateProvider); ok {
		traj.ResumeState = provider.GetResumeState()
	}
	return traj
}

// ToAgent restores agent state from a resume state snapshot.
func ToAgent(agent AgentStateRestorer, rs *ResumeState) {
	agent.RestoreResumeState(rs)
}

// LoadTrajectory loads a trajectory from disk.
func LoadTrajectory(path string) (FileTrajectory, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileTrajectory{}, fmt.Errorf("read trajectory: %w", err)
	}

	var traj FileTrajectory
	if err := json.Unmarshal(data, &traj); err != nil {
		return FileTrajectory{}, fmt.Errorf("parse trajectory: %w", err)
	}
	return traj, nil
}

// SaveTrajectoryToPath persists a trajectory as JSON files in a directory.
// It creates the directory if needed and writes trajectory.json.
func SaveTrajectoryToPath(traj FileTrajectory, dirPath string) error {
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("create trajectory directory: %w", err)
	}

	if err := SaveTrajectory(traj, filepath.Join(dirPath, "trajectory.json")); err != nil {
		return err
	}

	return nil
}

// LoadTrajectoryFromPath loads a trajectory previously saved with SaveTrajectoryToPath.
// It reads trajectory.json.
func LoadTrajectoryFromPath(dirPath string) (FileTrajectory, error) {
	traj, err := LoadTrajectory(filepath.Join(dirPath, "trajectory.json"))
	if err != nil {
		return FileTrajectory{}, err
	}

	return traj, nil
}
