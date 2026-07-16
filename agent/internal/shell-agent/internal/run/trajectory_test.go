package run

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

type stubAgent struct {
	messages []minisweagent.Message
}

func (s *stubAgent) Model() minisweagent.Model        { return nil }
func (s *stubAgent) Env() minisweagent.Environment    { return nil }
func (s *stubAgent) Messages() []minisweagent.Message { return s.messages }
func (s *stubAgent) Config() interface{}              { return nil }
func (s *stubAgent) Run(context.Context, string, ...minisweagent.RunOption) (string, string, error) {
	return "", "", nil
}

func TestMarshalTrajectory(t *testing.T) {
	traj := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}},
		ExitStatus: "0",
		Result:     "done",
		ExtraInfo:  map[string]interface{}{"key": "value"},
		Timestamp:  time.Now().UTC(),
	}

	data, err := MarshalTrajectory(traj)
	if err != nil {
		t.Fatalf("MarshalTrajectory error: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("MarshalTrajectory returned empty payload")
	}

	var roundTrip FileTrajectory
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("roundtrip unmarshal failed: %v", err)
	}
	if roundTrip.ExitStatus != traj.ExitStatus || roundTrip.Result != traj.Result {
		t.Fatalf("roundtrip mismatch: %+v", roundTrip)
	}
}

func TestSaveTrajectorySuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traj.json")
	traj := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}},
		ExitStatus: "0",
		Result:     "success",
		Timestamp:  time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	if err := SaveTrajectory(traj, path); err != nil {
		t.Fatalf("SaveTrajectory error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("saved JSON invalid: %s", string(data))
	}
}

func TestSaveTrajectoryMarshalError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traj.json")

	traj := FileTrajectory{
		ExitStatus: "0",
		Result:     "bad",
		ExtraInfo:  map[string]interface{}{"invalid": math.Inf(1)},
	}

	if err := SaveTrajectory(traj, path); err == nil {
		t.Fatalf("expected marshal error, got nil")
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file to be written, stat err = %v", err)
	}
}

func TestSaveTrajectoryCreatesMissingDirs(t *testing.T) {
	dir := t.TempDir()
	traj := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}},
		ExitStatus: "0",
		Result:     "done",
		Timestamp:  time.Date(2025, 7, 2, 12, 0, 0, 0, time.UTC),
	}
	path := filepath.Join(dir, "missing", "traj.json")

	if err := SaveTrajectory(traj, path); err != nil {
		t.Fatalf("SaveTrajectory should succeed, got error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	if len(data) == 0 {
		t.Fatalf("saved file is empty")
	}
	if !json.Valid(data) {
		t.Fatalf("saved JSON invalid: %s", string(data))
	}

	var loaded FileTrajectory
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal saved file: %v", err)
	}
	if loaded.ExitStatus != traj.ExitStatus {
		t.Errorf("ExitStatus mismatch: got %q, want %q", loaded.ExitStatus, traj.ExitStatus)
	}
	if loaded.Result != traj.Result {
		t.Errorf("Result mismatch: got %q, want %q", loaded.Result, traj.Result)
	}
}

func TestFromAgent(t *testing.T) {
	msgs := []minisweagent.Message{{Role: "user", Content: "hi"}}
	agent := &stubAgent{messages: msgs}

	before := time.Now().Add(-time.Second)
	traj := FromAgent(agent, "0", "ok", map[string]interface{}{"extra": true})
	after := time.Now().Add(time.Second)

	if len(traj.Messages) != len(msgs) {
		t.Fatalf("Messages length = %d, want %d", len(traj.Messages), len(msgs))
	}
	if traj.ExitStatus != "0" || traj.Result != "ok" {
		t.Fatalf("unexpected trajectory fields: %+v", traj)
	}
	if traj.ExtraInfo["extra"] != true {
		t.Fatalf("missing extra info: %+v", traj.ExtraInfo)
	}
	if traj.Timestamp.Before(before) || traj.Timestamp.After(after) {
		t.Fatalf("timestamp not within expected window: %v", traj.Timestamp)
	}
}

func TestLoadTrajectorySuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traj.json")
	original := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "assistant", Content: "result"}},
		ExitStatus: "done",
		Result:     "ok",
		Timestamp:  time.Date(2023, 5, 6, 7, 8, 9, 0, time.UTC),
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	loaded, err := LoadTrajectory(path)
	if err != nil {
		t.Fatalf("LoadTrajectory error: %v", err)
	}
	if loaded.ExitStatus != original.ExitStatus || loaded.Result != original.Result {
		t.Fatalf("loaded mismatch: %+v", loaded)
	}
	if got := loaded.Messages[0].Content; got != original.Messages[0].Content {
		t.Fatalf("message mismatch: %q", got)
	}
}

func TestLoadTrajectoryMissingFile(t *testing.T) {
	if _, err := LoadTrajectory(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestLoadTrajectoryInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatalf("write broken file: %v", err)
	}

	if _, err := LoadTrajectory(path); err == nil {
		t.Fatalf("expected parse error, got nil")
	}
}

func TestLoadTrajectoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume.json")

	original := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}},
		ExitStatus: "0",
		Result:     "success",
		Timestamp:  time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		ExtraInfo:  map[string]interface{}{"key": "value"},
		ResumeState: &ResumeState{
			Version:                      1,
			StepCounter:                  5,
			CommandsExecuted:             3,
			LastNonEmptyOutput:           "previous output",
			FinalizeRequested:            true,
			ConsecutiveFormatErrors:      2,
			ConsecutiveFinalizeReminders: 1,
			SystemPrompt:                 "system instructions",
			SystemPromptCached:           true,
		},
	}

	if err := SaveTrajectory(original, path); err != nil {
		t.Fatalf("SaveTrajectory error: %v", err)
	}

	loaded, err := LoadTrajectory(path)
	if err != nil {
		t.Fatalf("LoadTrajectory error: %v", err)
	}

	if loaded.ExitStatus != original.ExitStatus {
		t.Errorf("ExitStatus mismatch: got %q, want %q", loaded.ExitStatus, original.ExitStatus)
	}
	if loaded.Result != original.Result {
		t.Errorf("Result mismatch: got %q, want %q", loaded.Result, original.Result)
	}
	if !loaded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", loaded.Timestamp, original.Timestamp)
	}
	if len(loaded.Messages) != len(original.Messages) {
		t.Errorf("Messages length mismatch: got %d, want %d", len(loaded.Messages), len(original.Messages))
	} else if loaded.Messages[0].Content != original.Messages[0].Content {
		t.Errorf("Message content mismatch: got %q, want %q", loaded.Messages[0].Content, original.Messages[0].Content)
	}

	got := loaded.ResumeState
	want := original.ResumeState
	if got == nil {
		t.Fatalf("ResumeState is nil")
	}
	if got.Version != want.Version {
		t.Errorf("Version mismatch: got %d, want %d", got.Version, want.Version)
	}
	if got.StepCounter != want.StepCounter {
		t.Errorf("StepCounter mismatch: got %d, want %d", got.StepCounter, want.StepCounter)
	}
	if got.CommandsExecuted != want.CommandsExecuted {
		t.Errorf("CommandsExecuted mismatch: got %d, want %d", got.CommandsExecuted, want.CommandsExecuted)
	}
	if got.LastNonEmptyOutput != want.LastNonEmptyOutput {
		t.Errorf("LastNonEmptyOutput mismatch: got %q, want %q", got.LastNonEmptyOutput, want.LastNonEmptyOutput)
	}
	if got.FinalizeRequested != want.FinalizeRequested {
		t.Errorf("FinalizeRequested mismatch: got %v, want %v", got.FinalizeRequested, want.FinalizeRequested)
	}
	if got.ConsecutiveFormatErrors != want.ConsecutiveFormatErrors {
		t.Errorf("ConsecutiveFormatErrors mismatch: got %d, want %d", got.ConsecutiveFormatErrors, want.ConsecutiveFormatErrors)
	}
	if got.ConsecutiveFinalizeReminders != want.ConsecutiveFinalizeReminders {
		t.Errorf("ConsecutiveFinalizeReminders mismatch: got %d, want %d", got.ConsecutiveFinalizeReminders, want.ConsecutiveFinalizeReminders)
	}
	if got.SystemPrompt != want.SystemPrompt {
		t.Errorf("SystemPrompt mismatch: got %q, want %q", got.SystemPrompt, want.SystemPrompt)
	}
	if got.SystemPromptCached != want.SystemPromptCached {
		t.Errorf("SystemPromptCached mismatch: got %v, want %v", got.SystemPromptCached, want.SystemPromptCached)
	}
}

func TestSaveTrajectoryToPathSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-run")

	traj := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}},
		ExitStatus: "0",
		Result:     "success",
		ResumeState: &ResumeState{
			StepCounter:       5,
			FinalizeRequested: true,
			SystemPrompt:      "test prompt",
		},
	}

	if err := SaveTrajectoryToPath(traj, path); err != nil {
		t.Fatalf("SaveTrajectoryToPath error: %v", err)
	}

	trajFile := filepath.Join(path, "trajectory.json")
	if _, err := os.Stat(trajFile); os.IsNotExist(err) {
		t.Fatalf("trajectory.json does not exist")
	}

	loaded, err := LoadTrajectory(trajFile)
	if err != nil {
		t.Fatalf("LoadTrajectory error: %v", err)
	}
	if loaded.ExitStatus != traj.ExitStatus {
		t.Errorf("ExitStatus mismatch: got %q, want %q", loaded.ExitStatus, traj.ExitStatus)
	}
	if loaded.Result != traj.Result {
		t.Errorf("Result mismatch: got %q, want %q", loaded.Result, traj.Result)
	}
	if len(loaded.Messages) != len(traj.Messages) {
		t.Errorf("Messages length mismatch: got %d, want %d", len(loaded.Messages), len(traj.Messages))
	}
}

func TestSaveTrajectoryToPathNoResumeState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-run")

	traj := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}},
		ExitStatus: "1",
		Result:     "error",
		ResumeState: nil,
	}

	if err := SaveTrajectoryToPath(traj, path); err != nil {
		t.Fatalf("SaveTrajectoryToPath error: %v", err)
	}

	trajFile := filepath.Join(path, "trajectory.json")
	if _, err := os.Stat(trajFile); os.IsNotExist(err) {
		t.Fatalf("trajectory.json does not exist")
	}

	stateFile := filepath.Join(path, "state.json")
	if _, err := os.Stat(stateFile); !os.IsNotExist(err) {
		t.Fatalf("state.json should not exist but was found")
	}
}

func TestLoadTrajectoryFromPathRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-run")

	original := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "world"}},
		ExitStatus: "0",
		Result:     "done",
		Timestamp:  time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		ResumeState: &ResumeState{
			Version:                      1,
			StepCounter:                  10,
			CommandsExecuted:             7,
			LastNonEmptyOutput:           "some output",
			FinalizeRequested:            true,
			ConsecutiveFormatErrors:      1,
			ConsecutiveFinalizeReminders: 2,
			SystemPrompt:                 "system prompt",
			SystemPromptCached:           true,
		},
	}

	if err := SaveTrajectoryToPath(original, path); err != nil {
		t.Fatalf("SaveTrajectoryToPath error: %v", err)
	}

	loaded, err := LoadTrajectoryFromPath(path)
	if err != nil {
		t.Fatalf("LoadTrajectoryFromPath error: %v", err)
	}

	if loaded.ExitStatus != original.ExitStatus {
		t.Errorf("ExitStatus mismatch: got %q, want %q", loaded.ExitStatus, original.ExitStatus)
	}
	if loaded.Result != original.Result {
		t.Errorf("Result mismatch: got %q, want %q", loaded.Result, original.Result)
	}
	if !loaded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", loaded.Timestamp, original.Timestamp)
	}
	if len(loaded.Messages) != len(original.Messages) {
		t.Errorf("Messages length mismatch: got %d, want %d", len(loaded.Messages), len(original.Messages))
	} else {
		for i := range original.Messages {
			if loaded.Messages[i].Content != original.Messages[i].Content {
				t.Errorf("Message[%d] Content mismatch: got %q, want %q", i, loaded.Messages[i].Content, original.Messages[i].Content)
			}
		}
	}

	if loaded.ResumeState == nil {
		t.Fatalf("ResumeState is nil after roundtrip")
	}
	got := loaded.ResumeState
	want := original.ResumeState
	if got.Version != want.Version {
		t.Errorf("Version mismatch: got %d, want %d", got.Version, want.Version)
	}
	if got.StepCounter != want.StepCounter {
		t.Errorf("StepCounter mismatch: got %d, want %d", got.StepCounter, want.StepCounter)
	}
	if got.CommandsExecuted != want.CommandsExecuted {
		t.Errorf("CommandsExecuted mismatch: got %d, want %d", got.CommandsExecuted, want.CommandsExecuted)
	}
	if got.LastNonEmptyOutput != want.LastNonEmptyOutput {
		t.Errorf("LastNonEmptyOutput mismatch: got %q, want %q", got.LastNonEmptyOutput, want.LastNonEmptyOutput)
	}
	if got.FinalizeRequested != want.FinalizeRequested {
		t.Errorf("FinalizeRequested mismatch: got %v, want %v", got.FinalizeRequested, want.FinalizeRequested)
	}
	if got.ConsecutiveFormatErrors != want.ConsecutiveFormatErrors {
		t.Errorf("ConsecutiveFormatErrors mismatch: got %d, want %d", got.ConsecutiveFormatErrors, want.ConsecutiveFormatErrors)
	}
	if got.ConsecutiveFinalizeReminders != want.ConsecutiveFinalizeReminders {
		t.Errorf("ConsecutiveFinalizeReminders mismatch: got %d, want %d", got.ConsecutiveFinalizeReminders, want.ConsecutiveFinalizeReminders)
	}
	if got.SystemPrompt != want.SystemPrompt {
		t.Errorf("SystemPrompt mismatch: got %q, want %q", got.SystemPrompt, want.SystemPrompt)
	}
	if got.SystemPromptCached != want.SystemPromptCached {
		t.Errorf("SystemPromptCached mismatch: got %v, want %v", got.SystemPromptCached, want.SystemPromptCached)
	}
}

func TestLoadTrajectoryFromPathWithEmbeddedResumeState(t *testing.T) {
	dir := t.TempDir()
	trajPath := filepath.Join(dir, "trajectory.json")

	original := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "world"}},
		ExitStatus: "0",
		Result:     "done",
		Timestamp:  time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		ResumeState: &ResumeState{
			Version:                      1,
			StepCounter:                  10,
			CommandsExecuted:             7,
			LastNonEmptyOutput:           "some output",
			FinalizeRequested:            true,
			ConsecutiveFormatErrors:      1,
			ConsecutiveFinalizeReminders: 2,
			SystemPrompt:                 "system prompt",
			SystemPromptCached:           true,
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(trajPath, data, 0o644); err != nil {
		t.Fatalf("write trajectory.json: %v", err)
	}

	loaded, err := LoadTrajectoryFromPath(dir)
	if err != nil {
		t.Fatalf("LoadTrajectoryFromPath error: %v", err)
	}

	if loaded.ExitStatus != original.ExitStatus {
		t.Errorf("ExitStatus mismatch: got %q, want %q", loaded.ExitStatus, original.ExitStatus)
	}
	if loaded.Result != original.Result {
		t.Errorf("Result mismatch: got %q, want %q", loaded.Result, original.Result)
	}
	if !loaded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch: got %v, want %v", loaded.Timestamp, original.Timestamp)
	}
	if len(loaded.Messages) != len(original.Messages) {
		t.Errorf("Messages length mismatch: got %d, want %d", len(loaded.Messages), len(original.Messages))
	} else {
		for i := range original.Messages {
			if loaded.Messages[i].Content != original.Messages[i].Content {
				t.Errorf("Message[%d] Content mismatch: got %q, want %q", i, loaded.Messages[i].Content, original.Messages[i].Content)
			}
		}
	}

	if loaded.ResumeState == nil {
		t.Fatalf("ResumeState is nil")
	}
	got := loaded.ResumeState
	want := original.ResumeState
	if got.Version != want.Version {
		t.Errorf("Version mismatch: got %d, want %d", got.Version, want.Version)
	}
	if got.StepCounter != want.StepCounter {
		t.Errorf("StepCounter mismatch: got %d, want %d", got.StepCounter, want.StepCounter)
	}
	if got.CommandsExecuted != want.CommandsExecuted {
		t.Errorf("CommandsExecuted mismatch: got %d, want %d", got.CommandsExecuted, want.CommandsExecuted)
	}
	if got.LastNonEmptyOutput != want.LastNonEmptyOutput {
		t.Errorf("LastNonEmptyOutput mismatch: got %q, want %q", got.LastNonEmptyOutput, want.LastNonEmptyOutput)
	}
	if got.FinalizeRequested != want.FinalizeRequested {
		t.Errorf("FinalizeRequested mismatch: got %v, want %v", got.FinalizeRequested, want.FinalizeRequested)
	}
	if got.ConsecutiveFormatErrors != want.ConsecutiveFormatErrors {
		t.Errorf("ConsecutiveFormatErrors mismatch: got %d, want %d", got.ConsecutiveFormatErrors, want.ConsecutiveFormatErrors)
	}
	if got.ConsecutiveFinalizeReminders != want.ConsecutiveFinalizeReminders {
		t.Errorf("ConsecutiveFinalizeReminders mismatch: got %d, want %d", got.ConsecutiveFinalizeReminders, want.ConsecutiveFinalizeReminders)
	}
	if got.SystemPrompt != want.SystemPrompt {
		t.Errorf("SystemPrompt mismatch: got %q, want %q", got.SystemPrompt, want.SystemPrompt)
	}
	if got.SystemPromptCached != want.SystemPromptCached {
		t.Errorf("SystemPromptCached mismatch: got %v, want %v", got.SystemPromptCached, want.SystemPromptCached)
	}
}

func TestLoadTrajectoryFromPathMissingState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test-run")

	traj := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}},
		ExitStatus: "0",
		Result:     "ok",
		ResumeState: nil,
	}

	if err := SaveTrajectoryToPath(traj, path); err != nil {
		t.Fatalf("SaveTrajectoryToPath error: %v", err)
	}

	loaded, err := LoadTrajectoryFromPath(path)
	if err != nil {
		t.Fatalf("LoadTrajectoryFromPath error: %v", err)
	}

	if loaded.ResumeState != nil {
		t.Fatalf("expected ResumeState to be nil, got %+v", loaded.ResumeState)
	}
}

func TestSaveTrajectoryToPathCreatesDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new", "nested", "dir")

	traj := FileTrajectory{
		Messages:   []minisweagent.Message{{Role: "user", Content: "hello"}},
		ExitStatus: "0",
		Result:     "done",
	}

	if err := SaveTrajectoryToPath(traj, path); err != nil {
		t.Fatalf("SaveTrajectoryToPath error: %v", err)
	}

	trajFile := filepath.Join(path, "trajectory.json")
	if _, err := os.Stat(trajFile); os.IsNotExist(err) {
		t.Fatalf("trajectory.json does not exist at %s", trajFile)
	}

	data, err := os.ReadFile(trajFile)
	if err != nil {
		t.Fatalf("read trajectory.json: %v", err)
	}
	if !json.Valid(data) {
		t.Fatalf("trajectory.json is not valid JSON")
	}

	var loaded FileTrajectory
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal trajectory.json: %v", err)
	}
	if loaded.Result != traj.Result {
		t.Errorf("Result mismatch: got %q, want %q", loaded.Result, traj.Result)
	}
}

func TestMigrateResumeStateV1toV2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	// Create a v1 ResumeState JSON blob with all current fields but WITHOUT extra_instructions.
	rs := map[string]interface{}{
		"version":                         1,
		"step_counter":                    5,
		"commands_executed":               3,
		"last_non_empty_output":           "previous output",
		"finalize_requested":              true,
		"consecutive_format_errors":       2,
		"consecutive_finalize_reminders":  1,
		"system_prompt":                   "system instructions",
		"system_prompt_cached":            true,
	}
	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal v1 state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write v1 state: %v", err)
	}

	readData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read v1 state: %v", err)
	}

	// v1_to_v2 migration: parse JSON, add extra_instructions with empty default,
	// bump version to 2, and remarshal.
	v1ToV2 := func(input []byte) ([]byte, error) {
		var m map[string]interface{}
		if err := json.Unmarshal(input, &m); err != nil {
			return nil, err
		}
		m["extra_instructions"] = ""
		m["version"] = float64(2)
		return json.Marshal(m)
	}

	migrations := map[string]func([]byte) ([]byte, error){
		"v1_to_v2": v1ToV2,
	}

	migrated, err := MigrateResumeState(readData, 2, migrations)
	if err != nil {
		t.Fatalf("MigrateResumeState error: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(migrated, &result); err != nil {
		t.Fatalf("unmarshal migrated state: %v", err)
	}

	version, ok := result["version"]
	if !ok {
		t.Fatalf("version key missing from migrated state")
	}
	v, ok := version.(float64)
	if !ok || int(v) != 2 {
		t.Errorf("version = %v, want 2", version)
	}

	ei, ok := result["extra_instructions"]
	if !ok {
		t.Errorf("extra_instructions key missing from migrated state")
	} else if ei != "" {
		t.Errorf("extra_instructions = %q, want %q", ei, "")
	}
}

func TestMigrateResumeStateInvalidVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	rs := map[string]interface{}{
		"version":       99,
		"step_counter":  1,
		"system_prompt": "test",
	}
	data, err := json.Marshal(rs)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	readData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}

	_, err = MigrateResumeState(readData, 2, map[string]func([]byte) ([]byte, error){})
	if err == nil {
		t.Fatalf("expected error for invalid version, got nil")
	}
}
