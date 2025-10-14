package config

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTrajectoryRecorder_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "traj.jsonl")
	var tr TrajectoryRecorder
	if err := tr.Start(path); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if !tr.Enabled || tr.W == nil || tr.F == nil {
		t.Fatalf("recorder not initialized")
	}
	tr.Event("run_start", 0, map[string]any{"cfg": map[string]any{"x": 1}})
	tr.Event("rg_exec", 0, map[string]any{"total_lines": 3})
	tr.Event("rg_out_emitted", 0, map[string]any{"bytes": 10})
	tr.Event("run_end", 0, map[string]any{"exit_code": 0})
	tr.Close()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open trajectory: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	types := map[string]bool{}
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		if typ, ok := m["type"].(string); ok {
			types[typ] = true
		}
	}
	for _, need := range []string{"run_start", "rg_exec", "rg_out_emitted", "run_end"} {
		if !types[need] {
			t.Fatalf("missing event %q in JSONL: %v", need, types)
		}
	}
}

func TestTrajectoryRecorder_DisabledNoop(t *testing.T) {
	var tr TrajectoryRecorder // zero value disabled
	tr.Event("foo", 0, map[string]any{"a": 1})
	// no panic and no file created
}
