package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

// ToolCallMode selects how the LLM must emit tool calls.
type ToolCallMode string

const (
	// ToolCallModeJSON expects function-call JSON envelopes.
	ToolCallModeJSON ToolCallMode = "json"
	// ToolCallModeSimple uses the bracket-based micro syntax.
	ToolCallModeSimple ToolCallMode = "simple"
)

// Config holds CLI and environment configuration.
type Config struct {
	BaseURL         string
	APIKey          string
	APIKeyOverrides map[string]string
	Model           string
	MaxRounds       int
	CmdTimeoutSec   int
	MaxStdoutBytes  int
	MaxTranscript   int
	LogJSON         bool
	Verbose         bool
	DryRunRG        bool
	DryPattern      string
	TrajectoryPath  string
	NoTrajectory    bool
	// SessionID optionally scopes BEGIN/END markers; first 5 chars are used
	SessionID    string
	ToolCallMode ToolCallMode
}

// Logger provides simple structured logging to stderr.
type Logger struct {
	JSON bool
	V    bool
}

func (l Logger) Log(event string, kv map[string]any) {
	if l.JSON {
		m := map[string]any{"event": event}
		for k, v := range kv {
			m[k] = v
		}
		enc := json.NewEncoder(os.Stderr)
		_ = enc.Encode(m)
		return
	}
	b := &strings.Builder{}
	b.WriteString(event)
	if len(kv) > 0 {
		keys := make([]string, 0, len(kv))
		for k := range kv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(": ")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%s=%v", k, kv[k])
		}
	}
	log.Print(b.String())
}

func (l Logger) Info(msg string)  { l.Log("info", map[string]any{"msg": msg}) }
func (l Logger) Warn(msg string)  { l.Log("warn", map[string]any{"msg": msg}) }
func (l Logger) Error(msg string) { l.Log("error", map[string]any{"msg": msg}) }

// TrajectoryRecorder writes JSONL events describing the run.
type TrajectoryRecorder struct {
	Enabled bool
	F       *os.File
	W       *bufio.Writer
	EventID int64
	PID     int
	Path    string
	Warned  bool
}

func AutoTrajectoryPath(pid int) string {
	ts := time.Now().Format("20060102_150405")
	return fmt.Sprintf("trajectory-%s-%d.jsonl", ts, pid)
}

func (tr *TrajectoryRecorder) disableWithWarn(err error) {
	if tr.Enabled {
		tr.Enabled = false
		if !tr.Warned {
			fmt.Fprintf(os.Stderr, "trajectory_recorder_disabled: %v\n", err)
			tr.Warned = true
		}
	}
}

func (tr *TrajectoryRecorder) Start(path string) error {
	f, err := os.Create(path)
	if err != nil {
		tr.disableWithWarn(err)
		return err
	}
	tr.F = f
	tr.W = bufio.NewWriterSize(f, 64*1024)
	tr.Enabled = true
	tr.EventID = 0
	tr.PID = os.Getpid()
	tr.Path = path
	return nil
}

func (tr *TrajectoryRecorder) Close() {
	if tr.W != nil {
		if err := tr.W.Flush(); err != nil {
			tr.disableWithWarn(err)
		}
	}
	if tr.F != nil {
		if err := tr.F.Close(); err != nil {
			tr.disableWithWarn(err)
		}
	}
}

func (tr *TrajectoryRecorder) Event(typ string, round int, payload any) {
	if tr == nil || !tr.Enabled || tr.W == nil {
		return
	}
	tr.EventID++
	env := map[string]any{
		"type":     typ,
		"ts":       time.Now().UTC().Format(time.RFC3339Nano),
		"event_id": tr.EventID,
		"round":    round,
		"pid":      tr.PID,
	}
	if payload != nil {
		switch p := payload.(type) {
		case map[string]any:
			for k, v := range p {
				env[k] = v
			}
		default:
			// best-effort JSON expansion
			b, err := json.Marshal(p)
			if err == nil {
				var m map[string]any
				if err := json.Unmarshal(b, &m); err == nil {
					for k, v := range m {
						env[k] = v
					}
				}
			}
		}
	}
	b, err := json.Marshal(env)
	if err != nil {
		tr.disableWithWarn(err)
		return
	}
	if _, err := tr.W.Write(append(b, '\n')); err != nil {
		tr.disableWithWarn(err)
		return
	}
	if err := tr.W.Flush(); err != nil {
		tr.disableWithWarn(err)
		return
	}
}

func RedactConfig(cfg Config) map[string]any {
	return map[string]any{
		"baseURL":        cfg.BaseURL,
		"apiKey":         "REDACTED",
		"model":          cfg.Model,
		"maxRounds":      cfg.MaxRounds,
		"cmdTimeoutSec":  cfg.CmdTimeoutSec,
		"maxStdoutBytes": cfg.MaxStdoutBytes,
		"maxTranscript":  cfg.MaxTranscript,
		"logJSON":        cfg.LogJSON,
		"verbose":        cfg.Verbose,
		"dryRunRG":       cfg.DryRunRG,
		"dryPattern":     cfg.DryPattern,
		"trajectoryPath": cfg.TrajectoryPath,
		"noTrajectory":   cfg.NoTrajectory,
		"toolCallMode":   cfg.ToolCallMode,
	}
}

func GetenvOr(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}
