package ui

import (
	"io"
	"os"
	"sync"
)

// ProcessTimerManager serializes timer renders across multiple TerminalDisplay instances.
// It maintains an "active" timer to prevent footer collisions when multiple displays
// write to the same stdout.
type ProcessTimerManager struct {
	mu         sync.Mutex
	registered map[string]any
	activeID   string
	writer     io.Writer // Always os.Stdout for now
}

// NewProcessTimerManager creates a new process-level timer manager.
func NewProcessTimerManager() *ProcessTimerManager {
	return &ProcessTimerManager{
		registered: make(map[string]any),
		writer:     os.Stdout,
	}
}

// RegisterDisplay registers a display with the manager.
// If no active timer exists, this display becomes active.
func (m *ProcessTimerManager) RegisterDisplay(id string, display any) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registered[id] = display
	if m.activeID == "" {
		m.activeID = id
	}
}

// UnregisterDisplay removes a display from the manager.
// If it was the active timer, clears the active ID.
func (m *ProcessTimerManager) UnregisterDisplay(id string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.registered, id)
	if m.activeID == id {
		m.activeID = "" // Clear; next register will set a new active
	}
}

// RenderFooter renders the timer footer if the display ID is active.
// Only the active timer is rendered to prevent collisions on the footer line.
func (m *ProcessTimerManager) RenderFooter(id string, formattedTime string, row int) {
	m.RenderFooterLines(id, []string{formattedTime}, row)
}

// RenderFooterLines renders footer lines if the display ID is active.
func (m *ProcessTimerManager) RenderFooterLines(id string, lines []string, row int) {
	if m == nil {
		return
	}
	if len(lines) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.activeID {
		return // Only render if active; silence others
	}
	if row <= 0 {
		row = 999 - len(lines) + 1
	}
	RenderFooterLinesAtRow(lines, row, m.writer)
}

// RenderFooterLinesBelow renders footer lines relative to the active
// display's output cursor.
func (m *ProcessTimerManager) RenderFooterLinesBelow(id string, lines []string, offset int, allocate bool) {
	if m == nil || len(lines) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.activeID {
		return
	}
	RenderFooterLinesBelow(lines, offset, allocate, m.writer)
}
