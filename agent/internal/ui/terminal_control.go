// Package ui provides the terminal user interface components for mct-agent.
//
// Terminal control helpers that consolidate ANSI escape sequence generation
// for the TUI event bus refactor. Previously these sequences were spread
// across clearPreviousLinesLocked, flushLineLocked, resetTerminalLocked in
// terminal_display.go and RenderFooter in timer_manager.go.

package ui

import (
	"fmt"
	"io"
)

// SaveCursor writes the ANSI save-cursor escape sequence to w.
func SaveCursor(w io.Writer) {
	fmt.Fprint(w, ansiSaveCursor)
}

// RestoreCursor writes the ANSI restore-cursor escape sequence to w.
func RestoreCursor(w io.Writer) {
	fmt.Fprint(w, ansiRestoreCursor)
}

// ClearCurrentLine writes a carriage return followed by the ANSI clear-line
// escape sequence to w.
func ClearCurrentLine(w io.Writer) {
	fmt.Fprint(w, "\r")
	fmt.Fprint(w, ansiClearLine)
}

// ClearLinesAbove rewinds the cursor and clears n lines above the current
// position. If n is 0 or less the call returns immediately.
func ClearLinesAbove(n int, w io.Writer) {
	if n <= 0 {
		return
	}
	fmt.Fprint(w, "\r")
	if n > 1 {
		fmt.Fprintf(w, "\033[%dA", n-1)
	}
	for i := 0; i < n; i++ {
		fmt.Fprint(w, ansiClearLine)
		if i < n-1 {
			fmt.Fprint(w, "\n")
		}
	}
	if n > 1 {
		fmt.Fprintf(w, "\033[%dA", n-1)
	}
	fmt.Fprint(w, "\r")
}

// MoveCursorUp writes the ANSI escape sequence to move the cursor up by n
// lines to w.
func MoveCursorUp(n int, w io.Writer) {
	fmt.Fprintf(w, "\033[%dA", n)
}

// ResetTerminal writes the ANSI restore-cursor, clear-line, and reset escape
// sequences to w to restore the terminal to a clean state after a session.
func ResetTerminal(w io.Writer) {
	fmt.Fprint(w, ansiRestoreCursor)
	fmt.Fprint(w, ansiClearLine)
	fmt.Fprint(w, "\033[0m")
}

// RenderFooterLine renders a footer line at the bottom of the terminal. It
// saves the current cursor position, jumps to row 999 column 1, clears the
// line, writes text, and restores the cursor.
func RenderFooterLine(text string, w io.Writer) {
	SaveCursor(w)
	fmt.Fprint(w, "\033[999;1H")
	fmt.Fprint(w, ansiClearLine)
	fmt.Fprint(w, text)
	RestoreCursor(w)
}

// PositionCursorAt moves the cursor to the given row and column using an ANSI
// escape sequence.
func PositionCursorAt(row int, col int, w io.Writer) {
	fmt.Fprintf(w, "\033[%d;%dH", row, col)
}
