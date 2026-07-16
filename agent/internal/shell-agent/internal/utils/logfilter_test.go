package utils

import (
	"bytes"
	"testing"
)

func TestLogFilterWriterFiltersExcludedPatterns(t *testing.T) {
	var buf bytes.Buffer
	filter := NewLogFilterWriter(&buf, "filtered workspace sync start", "filtered workspace sync complete")

	// Write a line that should be filtered
	n, err := filter.Write([]byte(`time="2025-11-04T20:00:45-05:00" level=info msg="filtered workspace sync start" repo_root=/home/david/projects/undici` + "\n"))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	expectedLen := len(`time="2025-11-04T20:00:45-05:00" level=info msg="filtered workspace sync start" repo_root=/home/david/projects/undici` + "\n")
	if n != expectedLen {
		t.Errorf("Expected to report %d bytes written, got %d", expectedLen, n)
	}

	// Verify the buffer is empty (line was filtered)
	if buf.Len() > 0 {
		t.Errorf("Expected buffer to be empty, but got: %s", buf.String())
	}
}

func TestLogFilterWriterPassesThroughOtherLines(t *testing.T) {
	var buf bytes.Buffer
	filter := NewLogFilterWriter(&buf, "filtered workspace sync start", "filtered workspace sync complete")

	// Write a line that should pass through
	testLine := `time="2025-11-04T20:00:45-05:00" level=info msg="some other log" detail=value` + "\n"
	n, err := filter.Write([]byte(testLine))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if n != len(testLine) {
		t.Errorf("Expected to report %d bytes written, got %d", len(testLine), n)
	}

	if buf.String() != testLine {
		t.Errorf("Expected buffer to contain %q, but got: %q", testLine, buf.String())
	}
}

func TestLogFilterWriterFiltersMultiplePatterns(t *testing.T) {
	var buf bytes.Buffer
	filter := NewLogFilterWriter(&buf, "filtered workspace sync start", "filtered workspace sync complete")

	testCases := []struct {
		name       string
		line       string
		shouldPass bool
	}{
		{
			name:       "filters sync start",
			line:       `msg="filtered workspace sync start"` + "\n",
			shouldPass: false,
		},
		{
			name:       "filters sync complete",
			line:       `msg="filtered workspace sync complete"` + "\n",
			shouldPass: false,
		},
		{
			name:       "passes through other logs",
			line:       `msg="some other message"` + "\n",
			shouldPass: true,
		},
		{
			name:       "passes through logs with partial match in field name",
			line:       `field_sync_start="value"` + "\n",
			shouldPass: true,
		},
	}

	for _, tc := range testCases {
		buf.Reset()
		n, err := filter.Write([]byte(tc.line))
		if err != nil {
			t.Errorf("%s: Write failed: %v", tc.name, err)
			continue
		}

		if n != len(tc.line) {
			t.Errorf("%s: Expected to report %d bytes, got %d", tc.name, len(tc.line), n)
		}

		bufContent := buf.String()
		if tc.shouldPass && bufContent != tc.line {
			t.Errorf("%s: Expected line to pass through but buffer is empty", tc.name)
		}
		if !tc.shouldPass && bufContent != "" {
			t.Errorf("%s: Expected line to be filtered but got: %q", tc.name, bufContent)
		}
	}
}

func TestLogFilterWriterWithNoExclusions(t *testing.T) {
	var buf bytes.Buffer
	filter := NewLogFilterWriter(&buf) // No exclusions

	// All lines should pass through
	testLine := `time="2025-11-04T20:00:45-05:00" level=info msg="filtered workspace sync start"` + "\n"
	n, err := filter.Write([]byte(testLine))
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	if n != len(testLine) {
		t.Errorf("Expected to report %d bytes, got %d", len(testLine), n)
	}

	if buf.String() != testLine {
		t.Errorf("Expected buffer to contain %q, but got: %q", testLine, buf.String())
	}
}

func TestLogFilterWriterPreservesWriterInterface(t *testing.T) {
	var buf bytes.Buffer
	filter := NewLogFilterWriter(&buf, "excluded")

	// Test multiple writes
	line1 := "included message\n"
	line2 := "excluded message\n"
	line3 := "another included\n"

	for _, line := range []string{line1, line2, line3} {
		_, err := filter.Write([]byte(line))
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
	}

	expected := line1 + line3
	if buf.String() != expected {
		t.Errorf("Expected %q, got %q", expected, buf.String())
	}
}
