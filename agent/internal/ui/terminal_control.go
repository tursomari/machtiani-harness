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
	"strings"
)

// SaveCursor writes the ANSI save-cursor escape sequence to w.
func SaveCursor(w io.Writer) {
	fmt.Fprint(w, ansiSaveCursor)
}

// RestoreCursor writes the ANSI restore-cursor escape sequence to w.
func RestoreCursor(w io.Writer) {
	fmt.Fprint(w, ansiRestoreCursor)
}

// HideCursor hides the terminal's hardware cursor while a program-owned
// activity cursor is visible.
func HideCursor(w io.Writer) {
	fmt.Fprint(w, ansiHideCursor)
}

// ShowCursor restores the terminal's hardware cursor.
func ShowCursor(w io.Writer) {
	fmt.Fprint(w, ansiShowCursor)
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

// ResetTerminal resets terminal scroll/style state while preserving the current
// cursor. It saves the cursor immediately before resetting the scroll region so
// the restore cannot jump back to an older footer save point.
func ResetTerminal(w io.Writer) {
	fmt.Fprintf(w, "%s%s%s\r%s\033[0m", ansiSaveCursor, ansiResetScrollRegion, ansiRestoreCursor, ansiClearLine)
}

// SetScrollRegion reserves rows outside [top,bottom] from normal terminal
// scrolling for callers that need a fixed terminal region.
func SetScrollRegion(top int, bottom int, w io.Writer) {
	if top <= 0 || bottom <= 0 || bottom < top {
		return
	}
	fmt.Fprintf(w, "\033[%d;%dr\033[%d;1H", top, bottom, bottom)
}

// ResetScrollRegion restores the terminal's default full-screen scroll region.
func ResetScrollRegion(w io.Writer) {
	fmt.Fprintf(w, "%s%s%s", ansiSaveCursor, ansiResetScrollRegion, ansiRestoreCursor)
}

// RenderFooterLine renders a footer line at the bottom of the terminal. It
// saves the current cursor position, jumps to row 999 column 1, clears the
// line, writes text, and restores the cursor.
func RenderFooterLine(text string, w io.Writer) {
	RenderFooterLineAtRow(text, 999, w)
}

// RenderFooterLineAtRow renders a footer line at a specific terminal row.
func RenderFooterLineAtRow(text string, row int, w io.Writer) {
	if row <= 0 {
		row = 999
	}
	fmt.Fprintf(w, "%s\033[%d;1H%s%s%s", ansiSaveCursor, row, ansiClearLine, text, ansiRestoreCursor)
}

// RenderFooterLinesAtRow renders footer lines starting at a specific terminal
// row. All cursor movement and text is emitted in one write.
func RenderFooterLinesAtRow(lines []string, row int, w io.Writer) {
	if len(lines) == 0 {
		return
	}
	if row <= 0 {
		row = 999 - len(lines) + 1
	}
	var b strings.Builder
	b.WriteString(ansiSaveCursor)
	for i, line := range lines {
		fmt.Fprintf(&b, "\033[%d;1H%s%s", row+i, ansiClearLine, line)
	}
	b.WriteString(ansiRestoreCursor)
	fmt.Fprint(w, b.String())
}

// RenderFooterLinesBelow renders footer lines relative to the current output
// cursor. offset is the number of rows from the cursor to the first footer
// line. When allocate is true, the terminal is first advanced far enough to
// make the complete footer visible; this lets the output and footer scroll as
// one block when they reach the bottom of the viewport.
func RenderFooterLinesBelow(lines []string, offset int, allocate bool, w io.Writer) {
	if len(lines) == 0 {
		return
	}
	if offset < 1 {
		offset = 1
	}

	bottomOffset := offset + len(lines) - 1
	var b strings.Builder
	if allocate {
		for i := 0; i < bottomOffset; i++ {
			b.WriteString("\r\n")
		}
		if bottomOffset > 0 {
			fmt.Fprintf(&b, "\033[%dA", bottomOffset)
		}
	}
	b.WriteString(ansiSaveCursor)
	fmt.Fprintf(&b, "\033[%dB\r", offset)
	for i, line := range lines {
		b.WriteString(ansiClearLine)
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteString("\033[1B\r")
		}
	}
	b.WriteString(ansiRestoreCursor)
	fmt.Fprint(w, b.String())
}

// ClearFooterLinesBelow clears a relative footer without moving the output
// cursor. It is used immediately before ordinary output advances the footer's
// anchor.
func ClearFooterLinesBelow(lineCount int, offset int, w io.Writer) {
	if lineCount <= 0 {
		return
	}
	if offset < 1 {
		offset = 1
	}

	var b strings.Builder
	b.WriteString(ansiSaveCursor)
	fmt.Fprintf(&b, "\033[%dB\r", offset)
	for i := 0; i < lineCount; i++ {
		b.WriteString(ansiClearLine)
		if i < lineCount-1 {
			b.WriteString("\033[1B\r")
		}
	}
	b.WriteString(ansiRestoreCursor)
	fmt.Fprint(w, b.String())
}

// PositionCursorAt moves the cursor to the given row and column using an ANSI
// escape sequence.
func PositionCursorAt(row int, col int, w io.Writer) {
	fmt.Fprintf(w, "\033[%d;%dH", row, col)
}
