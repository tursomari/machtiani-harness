package run

import (
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func TestRenderCleanTranscriptSuccess(t *testing.T) {
	traj := FileTrajectory{
		Messages: []minisweagent.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "task"},
			{Role: "assistant", Content: "List files in repo"},
			{Role: "user", Content: "Observation:\nfile1.txt\nfile2.txt\n"},
			{Role: "assistant", Content: "Submit final answer"},
			{Role: "user", Content: "Observation:\n"},
		},
		ExtraInfo: map[string]interface{}{
			"subprocess_runs": []runRecord{
				{
					Intent: "List files in repo",
					Status: "success",
					Attempts: []attemptRecord{
						{Command: "ls -l", ReturnCode: 0, Output: "file1.txt\nfile2.txt\n"},
					},
				},
				{
					Intent: "Submit final answer",
					Status: "success",
					Attempts: []attemptRecord{
						{Command: "printf 'done' > /tmp/final", ReturnCode: 0},
					},
				},
			},
		},
	}

	out := RenderCleanTranscript(traj)
	if !strings.Contains(out, "ls -l") {
		t.Fatalf("expected transcript to include command, got:\n%s", out)
	}
	if strings.Contains(out, "Corrections") {
		t.Fatalf("expected corrections to be omitted, got:\n%s", out)
	}
	if !strings.Contains(out, "file1.txt") {
		t.Fatalf("expected output snippet to be present, got:\n%s", out)
	}
}

func TestRenderCleanTranscriptFailure(t *testing.T) {
	traj := FileTrajectory{
		Messages: []minisweagent.Message{
			{Role: "assistant", Content: "Run integration tests"},
		},
		ExtraInfo: map[string]interface{}{
			"subprocess_runs": []runRecord{
				{
					Intent: "Run integration tests",
					Status: "failed",
					Attempts: []attemptRecord{
						{Command: "go test ./...", ReturnCode: 1, Output: "--- FAIL: TestExample\n", Error: "non-zero exit code 1"},
					},
				},
			},
		},
	}

	out := RenderCleanTranscript(traj)
	if !strings.Contains(out, "Failed") {
		t.Fatalf("expected failure summary, got:\n%s", out)
	}
	if !strings.Contains(out, "go test ./...") {
		t.Fatalf("expected command to be included, got:\n%s", out)
	}
	if strings.Contains(out, "Attempts") {
		t.Fatalf("expected retry details to be omitted, got:\n%s", out)
	}
}

func TestRenderCleanTranscriptSkipsFormatErrors(t *testing.T) {
	traj := FileTrajectory{
		Messages: []minisweagent.Message{
			{Role: "assistant", Content: "```bash\neq 1\n```"},
			{Role: "user", Content: "Please respond with a natural language description"},
			{Role: "assistant", Content: "List files"},
			{Role: "user", Content: "Observation:\nalpha\n"},
		},
		ExtraInfo: map[string]interface{}{
			"subprocess_runs": []runRecord{
				{
					Intent:   "List files",
					Status:   "success",
					Attempts: []attemptRecord{{Command: "ls", ReturnCode: 0, Output: "alpha\n"}},
				},
			},
		},
	}

	out := RenderCleanTranscript(traj)
	if strings.Contains(out, "Please respond with") {
		t.Fatalf("expected format error guidance to be omitted, got:\n%s", out)
	}
	if !strings.Contains(out, "ls") {
		t.Fatalf("expected successful step to remain, got:\n%s", out)
	}
}

func TestRenderSimpleTranscriptReturnsResultOnSuccess(t *testing.T) {
	traj := FileTrajectory{
		ExitStatus: "Submitted",
		Result:     "final answer",
	}

	got := RenderSimpleTranscript(traj)
	if got != "final answer" {
		t.Fatalf("RenderSimpleTranscript = %q, want %q", got, "final answer")
	}
}

func TestRenderSimpleTranscriptFallsBackWhenResultMissing(t *testing.T) {
	traj := newSimpleTestTrajectory("Submitted", "", runRecord{
		Intent: "List files",
		Status: "success",
		Attempts: []attemptRecord{{
			Command:    "ls",
			ReturnCode: 0,
			Output:     "file.txt\nREADME.md",
		}},
	})

	got := RenderSimpleTranscript(traj)
	if !strings.Contains(got, "[BEGIN EXECUTION]") {
		t.Fatalf("RenderSimpleTranscript should fall back to execution steps, got %q", got)
	}
	if !strings.Contains(got, "file.txt") {
		t.Fatalf("RenderSimpleTranscript should include command output when falling back; got:\n%s", got)
	}
}

func TestRenderSimpleTranscriptFallsBackOnErrorExit(t *testing.T) {
	traj := newSimpleTestTrajectory("Error", "some result", runRecord{
		Intent: "Run build",
		Status: "error",
		Attempts: []attemptRecord{{
			Command:    "make build",
			ReturnCode: 1,
			Error:      "build failed",
		}},
	})

	got := RenderSimpleTranscript(traj)
	if strings.TrimSpace(got) == "some result" {
		t.Fatalf("RenderSimpleTranscript should not return only the final result on error, got %q", got)
	}
	if !strings.Contains(got, "[BEGIN EXECUTION]") {
		t.Fatalf("RenderSimpleTranscript should include execution steps on error; got %q", got)
	}
}

func TestRenderSimpleTranscriptUsesConfiguredStatusGlyphs(t *testing.T) {
	success := newSimpleTestTrajectory("Submitted", "", runRecord{
		Intent: "List files", Status: "success",
		Attempts: []attemptRecord{{Command: "ls", ReturnCode: 0}},
	})
	ascii := presentation.GlyphsForMode(presentation.GlyphASCII)
	if got := RenderSimpleTranscript(success, ascii); !strings.Contains(got, "[OK] Success") || strings.Contains(got, "✓") {
		t.Fatalf("ASCII success status mismatch: %q", got)
	}

	failure := newSimpleTestTrajectory("Error", "", runRecord{
		Intent: "Build", Status: "error",
		Attempts: []attemptRecord{{Command: "make", ReturnCode: 1, Error: "build failed"}},
	})
	if got := RenderSimpleTranscript(failure, ascii); !strings.Contains(got, "[ERROR] build failed") || strings.Contains(got, "✗") {
		t.Fatalf("ASCII failure status mismatch: %q", got)
	}
}

func TestRenderSimpleTranscriptTreatsWhitespaceResultAsEmpty(t *testing.T) {
	traj := newSimpleTestTrajectory("Submitted", " \n", runRecord{
		Intent: "List files",
		Status: "success",
		Attempts: []attemptRecord{{
			Command:    "ls",
			ReturnCode: 0,
			Output:     "file.txt\nREADME.md",
		}},
	})

	got := RenderSimpleTranscript(traj)
	if !strings.Contains(got, "[BEGIN EXECUTION]") {
		t.Fatalf("RenderSimpleTranscript should fall back to execution steps when result is whitespace, got %q", got)
	}
}

func newSimpleTestTrajectory(exitStatus, result string, record runRecord) FileTrajectory {
	intent := strings.TrimSpace(record.Intent)
	if intent == "" {
		intent = "Do something"
	}

	return FileTrajectory{
		Messages: []minisweagent.Message{
			{Role: "assistant", Content: intent},
		},
		ExitStatus: exitStatus,
		Result:     result,
		ExtraInfo: map[string]interface{}{
			"subprocess_runs": []runRecord{record},
		},
	}
}
