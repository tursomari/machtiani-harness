package prompt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tursomari/machtiani/agent/internal/core/artifacts"
	shellagent "github.com/tursomari/machtiani/agent/internal/shell-agent"
	"github.com/tursomari/machtiani/agent/internal/shellaction"
	"github.com/tursomari/machtiani/agent/internal/shellbridge"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

type shellActionJournal struct {
	path         string
	sessionID    string
	turn         int
	nextSequence int64
}

func newShellActionJournal(req shellagent.Request) (*shellActionJournal, error) {
	var path string
	if strings.TrimSpace(req.TrajectoryBaseDir) != "" {
		path = filepath.Join(req.TrajectoryBaseDir, "actions.jsonl")
	} else {
		var err error
		path, err = artifacts.ShellAgentActionsPath(req.SessionID, req.PlannerTurn)
		if err != nil {
			return nil, err
		}
	}

	journal := &shellActionJournal{path: path, sessionID: req.SessionID, turn: req.PlannerTurn, nextSequence: 1}
	data, err := os.ReadFile(path)
	if err == nil {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			complete := bytes.LastIndexByte(data, '\n') + 1
			if truncateErr := os.Truncate(path, int64(complete)); truncateErr != nil {
				return nil, fmt.Errorf("discard partial shell action journal record: %w", truncateErr)
			}
			data = data[:complete]
		}
		records, parseErr := shellaction.ParseJournal(data)
		if parseErr != nil {
			return nil, parseErr
		}
		journal.nextSequence = shellaction.LastSequence(records) + 1
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read shell action journal: %w", err)
	}
	return journal, nil
}

func (j *shellActionJournal) Append(action shellbridge.ActionMessage) error {
	if j == nil {
		return nil
	}
	record := shellaction.Record{
		Version:          1,
		SessionID:        strings.TrimSpace(j.sessionID),
		Turn:             j.turn,
		Sequence:         j.nextSequence,
		Description:      strings.TrimSpace(action.Description),
		Command:          strings.TrimSpace(action.Command),
		Step:             action.CommandsExecuted,
		StepLimit:        action.StepLimit,
		RemainingSteps:   action.RemainingSteps,
		CommandsExecuted: action.CommandsExecuted,
	}
	data, err := shellaction.EncodeRecord(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return fmt.Errorf("create shell action journal directory: %w", err)
	}
	file, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open shell action journal: %w", err)
	}
	written, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append shell action journal: %w", writeErr)
	}
	if written != len(data) {
		return fmt.Errorf("append shell action journal: %w", io.ErrShortWrite)
	}
	if closeErr != nil {
		return fmt.Errorf("close shell action journal: %w", closeErr)
	}
	j.nextSequence++
	return nil
}

func observeShellAction(action shellbridge.ActionMessage, diagnostics io.Writer, journal *shellActionJournal) {
	if err := journal.Append(action); err != nil {
		fmt.Fprintf(diagnostics, "intercept: append shell action journal: %v\n", err)
	}
}

func interceptShellAction(ctx context.Context, line string, diagnostics io.Writer) bool {
	if !strings.HasPrefix(line, shellbridge.ActionPrefix) {
		return false
	}
	rest := strings.TrimPrefix(line, shellbridge.ActionPrefix)
	var action shellbridge.ActionMessage
	if err := json.Unmarshal([]byte(rest), &action); err != nil {
		fmt.Fprintf(diagnostics, "intercept: parse action json: %v\n", err)
		return true
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rest), &payload); err != nil {
		fmt.Fprintf(diagnostics, "intercept: parse action payload: %v\n", err)
		return true
	}
	evt := trajectory.Event{Kind: "shell-agent.action", Payload: payload}
	if err := trajectory.EmitFromContext(ctx, evt); err != nil {
		fmt.Fprintf(diagnostics, "intercept: emit trajectory event: %v\n", err)
	}
	return true
}

// runShellAgentWithInterception invokes shellagent.Run while intercepting
// lines written to os.Stdout. Lines prefixed with shellbridge.ActionPrefix are
// parsed as JSON and emitted as trajectory events (kind "shell-agent.action");
// all other lines are forwarded to the original stdout unchanged.
func runShellAgentWithInterception(ctx context.Context, req shellagent.Request) (shellagent.Result, error) {
	original := os.Stdout
	journal, journalErr := newShellActionJournal(req)
	if journalErr != nil {
		fmt.Fprintf(os.Stderr, "intercept: initialize shell action journal: %v\n", journalErr)
	}
	existingObserver := req.ActionObserver
	req.ActionObserver = func(action shellbridge.ActionMessage) {
		if existingObserver != nil {
			existingObserver(action)
		}
		observeShellAction(action, os.Stderr, journal)
	}

	r, w, err := os.Pipe()
	if err != nil {
		return shellagent.Result{}, fmt.Errorf("create stdout pipe: %w", err)
	}

	os.Stdout = w

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		// Allow long lines (up to 1 MiB) so large action payloads are not
		// silently truncated.
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if interceptShellAction(ctx, line, os.Stderr) {
				continue
			}
			fmt.Fprintf(original, "%s\n", line)
		}
		if err := scanner.Err(); err != nil && err != io.EOF {
			fmt.Fprintf(original, "intercept: scanner: %v\n", err)
		}
	}()

	res, runErr := shellagent.Run(ctx, req)

	os.Stdout = original
	_ = w.Close()
	wg.Wait()

	return res, runErr
}
