package config

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

// Config holds CLI and environment configuration.
type Config struct {
	Reason          string
	FilePaths       []string
	BaseURL         string
	APIKey          string
	APIKeyOverrides map[string]string
	Model           string
	MaxRounds       int
	TimeoutSec      int
	MaxTranscript   int
	MaxLinesPerFile int
	LogJSON         bool
	Verbose         bool
	ErrorStreamPath string
	ErrorStream     io.WriteCloser
	TrajectoryPath  string
	NoTrajectory    bool
}

// Logger provides simple structured logging to stderr.
type Logger struct {
	JSON        bool
	V           bool
	ErrorStream io.Writer
}

func (l Logger) Log(event string, kv map[string]any) {
	if kv == nil {
		kv = map[string]any{}
	}
	if l.JSON {
		l.writeJSON(os.Stderr, event, kv)
	} else {
		log.Print(formatKV(event, kv))
	}
	if l.ErrorStream != nil && event == "error" {
		if l.JSON {
			l.writeJSON(l.ErrorStream, event, kv)
		} else {
			_, _ = fmt.Fprintln(l.ErrorStream, formatKV(event, kv))
		}
	}
}

func (l Logger) Info(msg string)  { l.Log("info", map[string]any{"msg": msg}) }
func (l Logger) Warn(msg string)  { l.Log("warn", map[string]any{"msg": msg}) }
func (l Logger) Error(msg string) { l.Log("error", map[string]any{"msg": msg}) }
func (l Logger) Debug(event string, kv map[string]any) {
	if !l.V {
		return
	}
	l.Log(event, kv)
}

func (l Logger) ErrorWithFields(msg string, kv map[string]any) {
	fields := map[string]any{"msg": msg}
	for k, v := range kv {
		fields[k] = v
	}
	l.Log("error", fields)
}

type nopWriteCloser struct {
	io.Writer
}

func (n nopWriteCloser) Close() error { return nil }

func OpenErrorStream(path string) (io.WriteCloser, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, nil
	}
	if trimmed == "-" {
		return nopWriteCloser{Writer: os.Stderr}, nil
	}
	if strings.HasPrefix(trimmed, "/dev/fd/") || strings.HasPrefix(trimmed, "/proc/self/fd/") {
		return os.OpenFile(trimmed, os.O_WRONLY, 0)
	}
	return os.OpenFile(trimmed, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}

func formatKV(event string, kv map[string]any) string {
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
	return b.String()
}

func (l Logger) writeJSON(w io.Writer, event string, kv map[string]any) {
	m := map[string]any{"event": event}
	for k, v := range kv {
		m[k] = v
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(m)
}

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
		"reason":          cfg.Reason,
		"filePaths":       cfg.FilePaths,
		"baseURL":         cfg.BaseURL,
		"apiKey":          "REDACTED",
		"model":           cfg.Model,
		"maxRounds":       cfg.MaxRounds,
		"timeoutSec":      cfg.TimeoutSec,
		"maxTranscript":   cfg.MaxTranscript,
		"maxLinesPerFile": cfg.MaxLinesPerFile,
		"logJSON":         cfg.LogJSON,
		"verbose":         cfg.Verbose,
		"errorStreamPath": cfg.ErrorStreamPath,
		"trajectoryPath":  cfg.TrajectoryPath,
		"noTrajectory":    cfg.NoTrajectory,
	}
}
