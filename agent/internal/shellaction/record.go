package shellaction

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Record is an announced shell command persisted for live display. The
// journal records command starts; it is not resumable execution state.
type Record struct {
	Version          int    `json:"v"`
	SessionID        string `json:"session_id,omitempty"`
	Turn             int    `json:"turn"`
	Sequence         int64  `json:"seq"`
	Description      string `json:"description,omitempty"`
	Command          string `json:"command,omitempty"`
	Step             int    `json:"step,omitempty"`
	StepLimit        int    `json:"step_limit,omitempty"`
	RemainingSteps   int    `json:"remaining_steps,omitempty"`
	CommandsExecuted int    `json:"commands_executed,omitempty"`
}

// EncodeRecord returns one newline-framed JSON journal record.
func EncodeRecord(record Record) ([]byte, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal shell action: %w", err)
	}
	return append(data, '\n'), nil
}

// ParseJournal parses complete newline-framed records. A trailing partial line
// is held for a future read rather than treated as corruption.
func ParseJournal(data []byte) ([]Record, error) {
	lines := bytes.Split(data, []byte{'\n'})
	complete := len(lines)
	if complete > 0 && len(lines[complete-1]) > 0 {
		complete--
	}

	records := make([]Record, 0, complete)
	for index, line := range lines[:complete] {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record Record
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("parse shell action line %d: %w", index+1, err)
		}
		records = append(records, record)
	}
	return records, nil
}

// LastSequence returns the greatest positive sequence in records.
func LastSequence(records []Record) int64 {
	var last int64
	for _, record := range records {
		if record.Sequence > last {
			last = record.Sequence
		}
	}
	return last
}
