package ui

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// ProcessTimerManager serializes timer renders across multiple TerminalDisplay instances.
// It maintains an "active" timer to prevent footer collisions when multiple displays
// write to the same stdout.
type ProcessTimerManager struct {
	mu         sync.Mutex
	registered map[string]*TerminalDisplay
	activeID   string
	writer     io.Writer // Always os.Stdout for now
}

// NewProcessTimerManager creates a new process-level timer manager.
func NewProcessTimerManager() *ProcessTimerManager {
	return &ProcessTimerManager{
		registered: make(map[string]*TerminalDisplay),
		writer:     os.Stdout,
	}
}

// RegisterDisplay registers a display with the manager.
// If no active timer exists, this display becomes active.
func (m *ProcessTimerManager) RegisterDisplay(id string, display *TerminalDisplay) {
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
func (m *ProcessTimerManager) RenderFooter(id string, formattedTime string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.activeID {
		return // Only render if active; silence others
	}
	// Standard render logic (ANSI codes for cursor positioning and timer display)
	fmt.Fprint(m.writer, ansiSaveCursor)
	fmt.Fprint(m.writer, "\033[999;1H") // Position at row 999, column 1 (footer)
	fmt.Fprint(m.writer, ansiClearLine)
	fmt.Fprintf(m.writer, "%s%s%s", ansiGray, formattedTime, ansiReset)
	fmt.Fprint(m.writer, ansiRestoreCursor)
}
