package ui

import (
	"bytes"
	"testing"
)

type countingWriter struct {
	bytes.Buffer
	writes int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(p)
}

func TestRenderFooterLineAtRowWritesAtomically(t *testing.T) {
	var buf countingWriter

	RenderFooterLineAtRow("footer", 24, &buf)

	got := buf.String()
	want := "\033[s\033[24;1H\033[2Kfooter\033[u"
	if got != want {
		t.Fatalf("footer render escape sequence mismatch\nwant: %q\n got: %q", want, got)
	}
	if buf.writes != 1 {
		t.Fatalf("footer render should use one write, got %d", buf.writes)
	}
}

func TestRenderFooterLinesAtRowWritesAtomically(t *testing.T) {
	var buf countingWriter

	RenderFooterLinesAtRow([]string{"top", "bottom"}, 23, &buf)

	got := buf.String()
	want := "\033[s\033[23;1H\033[2Ktop\033[24;1H\033[2Kbottom\033[u"
	if got != want {
		t.Fatalf("footer render escape sequence mismatch\nwant: %q\n got: %q", want, got)
	}
	if buf.writes != 1 {
		t.Fatalf("footer render should use one write, got %d", buf.writes)
	}
}

func TestSetScrollRegionWritesAtomically(t *testing.T) {
	var buf countingWriter

	SetScrollRegion(1, 23, &buf)

	got := buf.String()
	want := "\033[1;23r\033[23;1H"
	if got != want {
		t.Fatalf("scroll region escape sequence mismatch\nwant: %q\n got: %q", want, got)
	}
	if buf.writes != 1 {
		t.Fatalf("scroll region should use one write, got %d", buf.writes)
	}
}

func TestResetTerminalPreservesCurrentCursor(t *testing.T) {
	var buf countingWriter

	ResetTerminal(&buf)

	got := buf.String()
	want := "\033[s\033[r\033[u\r\033[2K\033[0m"
	if got != want {
		t.Fatalf("terminal reset escape sequence mismatch\nwant: %q\n got: %q", want, got)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte(ansiSaveCursor+ansiResetScrollRegion+ansiRestoreCursor)) {
		t.Fatalf("terminal reset should restore only the cursor saved in the same sequence: %q", got)
	}
}

func TestHardwareCursorVisibilityControls(t *testing.T) {
	var buf countingWriter

	HideCursor(&buf)
	ShowCursor(&buf)

	if got, want := buf.String(), ansiHideCursor+ansiShowCursor; got != want {
		t.Fatalf("cursor visibility controls = %q, want %q", got, want)
	}
}

func TestRenderFooterLinesBelowAllocatesRelativeBlock(t *testing.T) {
	var buf countingWriter

	RenderFooterLinesBelow([]string{"top", "bottom"}, 1, true, &buf)

	got := buf.String()
	want := "\r\n\r\n\033[2A\033[s\033[1B\r\033[2Ktop\033[1B\r\033[2Kbottom\033[u"
	if got != want {
		t.Fatalf("relative footer render escape sequence mismatch\nwant: %q\n got: %q", want, got)
	}
	if buf.writes != 1 {
		t.Fatalf("relative footer render should use one write, got %d", buf.writes)
	}
}

func TestRenderFooterLinesBelowStreamingOffsetPreservesBlankRow(t *testing.T) {
	var buf countingWriter

	RenderFooterLinesBelow([]string{"top", "bottom"}, 2, true, &buf)

	got := buf.String()
	want := "\r\n\r\n\r\n\033[3A\033[s\033[2B\r\033[2Ktop\033[1B\r\033[2Kbottom\033[u"
	if got != want {
		t.Fatalf("streaming footer render escape sequence mismatch\nwant: %q\n got: %q", want, got)
	}
}

func TestClearFooterLinesBelowWritesAtomically(t *testing.T) {
	var buf countingWriter

	ClearFooterLinesBelow(2, 1, &buf)

	got := buf.String()
	want := "\033[s\033[1B\r\033[2K\033[1B\r\033[2K\033[u"
	if got != want {
		t.Fatalf("relative footer clear escape sequence mismatch\nwant: %q\n got: %q", want, got)
	}
	if buf.writes != 1 {
		t.Fatalf("relative footer clear should use one write, got %d", buf.writes)
	}
}
