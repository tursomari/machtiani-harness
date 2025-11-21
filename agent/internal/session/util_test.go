package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchDiffForTranscript_NoLimitReturnsFullDiff(t *testing.T) {
	tempDir := t.TempDir()
	patchPath := filepath.Join(tempDir, "large.patch")
	original := strings.Repeat("+example line\n", 3000) // > 12k characters once trimmed
	if err := os.WriteFile(patchPath, []byte(original), 0o644); err != nil {
		t.Fatalf("failed to write patch: %v", err)
	}

	got, err := patchDiffForTranscript(patchPath, 0)
	if err != nil {
		t.Fatalf("patchDiffForTranscript returned error: %v", err)
	}

	expected := strings.TrimSpace(original)
	if got != expected {
		t.Fatalf("diff mismatch\nwant len=%d\n got len=%d", len(expected), len(got))
	}
	if strings.Contains(got, "[diff truncated]") {
		t.Fatalf("unexpected truncation marker in diff output")
	}
}

func TestPatchDiffForTranscript_HonorsPositiveLimit(t *testing.T) {
	tempDir := t.TempDir()
	patchPath := filepath.Join(tempDir, "limited.patch")
	original := "first line\nsecond line\nthird line\n"
	if err := os.WriteFile(patchPath, []byte(original), 0o644); err != nil {
		t.Fatalf("failed to write patch: %v", err)
	}

	got, err := patchDiffForTranscript(patchPath, 10)
	if err != nil {
		t.Fatalf("patchDiffForTranscript returned error: %v", err)
	}
	if !strings.HasSuffix(got, "[diff truncated]") {
		t.Fatalf("expected truncation marker, got: %q", got)
	}
}
