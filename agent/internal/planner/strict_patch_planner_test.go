package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	patcher "github.com/tursomari/machtiani/agent/internal/mct/patcher"
)

func TestRunStrictPatchFlowLoadsFileSnapshot(t *testing.T) {
	repoRoot := t.TempDir()
	relPath := "LICENSE"
	content := "Alpha\nBeta\nGamma\n"
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot})
	client.UpdateProgress(Progress{SuccessFiles: []string{"LICENSE"}, AppliedPatches: 1})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, prompt string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			if !strings.Contains(prompt, "strict patch path selector") {
				t.Fatalf("path selection prompt missing guidance: %q", prompt)
			}
			if !strings.Contains(prompt, "Files already patched successfully this session") {
				t.Fatalf("expected path selection prompt to mention prior successes: %q", prompt)
			}
			return `{"path":"LICENSE","reason":"Update copyright"}`, nil
		case 2:
			if !strings.Contains(prompt, "File status: exists on disk.") {
				t.Fatalf("patch prompt missing file status: %q", prompt)
			}
			if !strings.Contains(prompt, "Files already patched successfully this session") {
				t.Fatalf("expected patch prompt to mention prior successes: %q", prompt)
			}
			if !strings.Contains(prompt, "1 | Alpha") || !strings.Contains(prompt, "2 | Beta") || !strings.Contains(prompt, "3 | Gamma") {
				t.Fatalf("patch prompt missing numbered file snapshot: %q", prompt)
			}
			if !strings.Contains(prompt, "```text") {
				t.Fatalf("patch prompt should fence contents: %q", prompt)
			}
			response := `{
  "metadata": {"description": "Add year"},
  "edits": [
    {
      "path": "LICENSE",
      "mode": "patch",
      "start_line": 2,
      "end_line": 2,
      "new_content": "Beta 2025\n"
    }
  ]
}`
			return response, nil
		default:
			marker := "Previous attempt failed because: "
			reason := "<unknown>"
			if idx := strings.LastIndex(prompt, marker); idx >= 0 {
				reason = prompt[idx+len(marker):]
				if nl := strings.IndexByte(reason, '\n'); nl >= 0 {
					reason = reason[:nl]
				}
			}
			t.Fatalf("unexpected chat invocation %d, reason: %q", call, reason)
			return "", nil
		}
	}

	payload, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 10, "")
	if err != nil {
		t.Fatalf("runStrictPatchFlow error: %v", err)
	}
	if call != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", call)
	}
	if !strings.Contains(payload, "\n  \"metadata\"") {
		t.Fatalf("expected pretty-printed JSON, got %q", payload)
	}

	var instr patcher.Instructions
	if err := json.Unmarshal([]byte(payload), &instr); err != nil {
		t.Fatalf("decode returned payload: %v", err)
	}
	if len(instr.Edits) != 1 {
		t.Fatalf("expected one edit, got %d", len(instr.Edits))
	}
	edit := instr.Edits[0]
	if edit.Path != "LICENSE" {
		t.Fatalf("expected normalized path LICENSE, got %q", edit.Path)
	}
	if edit.Mode != patcher.ModePatch {
		t.Fatalf("expected mode patch, got %q", edit.Mode)
	}
	if got, want := edit.StartLine, 2; got != want {
		t.Fatalf("start_line = %d, want %d", got, want)
	}
	if got, want := edit.EndLine, 2; got != want {
		t.Fatalf("end_line = %d, want %d", got, want)
	}
	if got, want := edit.NewContent, "Beta 2025\n"; got != want {
		t.Fatalf("new_content = %q, want %q", got, want)
	}
}

func TestStrictPatchNormalizationHandlesReplacementAlias(t *testing.T) {
	repoRoot := t.TempDir()
	relPath := "LICENSE"
	content := "MIT License\n\nCopyright (c) Matteo Collina and Undici contributors\n\nPermission is hereby granted, free of charge, to any person obtaining a copy\n"
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, prompt string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			return `{"path":"LICENSE","reason":"root license"}`, nil
		case 2:
			if !strings.Contains(prompt, "start_line") {
				t.Fatalf("expected start_line guidance, got %q", prompt)
			}
			return `{
	  "metadata": {"description": "Update LICENSE year"},
	  "edits": [
	    {
	      "path": "LICENSE",
	      "mode": "patch",
	      "start_line": 3,
	      "end_line": 3,
	      "replacement": "Copyright (c) 2020-2025 Matteo Collina and Undici contributors\n"
	    }
	  ]
	}`, nil
		default:
			t.Fatalf("unexpected invocation %d", call)
			return "", nil
		}
	}

	payload, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 5, "")
	if err != nil {
		t.Fatalf("runStrictPatchFlow error: %v", err)
	}
	var instr patcher.Instructions
	if err := json.Unmarshal([]byte(payload), &instr); err != nil {
		t.Fatalf("decode returned payload: %v", err)
	}
	if len(instr.Edits) != 1 {
		t.Fatalf("expected one edit, got %d", len(instr.Edits))
	}
	ed := instr.Edits[0]
	if got, want := ed.StartLine, 3; got != want {
		t.Fatalf("start_line = %d, want %d", got, want)
	}
	if got, want := ed.EndLine, 3; got != want {
		t.Fatalf("end_line = %d, want %d", got, want)
	}
	if got, want := ed.NewContent, "Copyright (c) 2020-2025 Matteo Collina and Undici contributors\n"; got != want {
		t.Fatalf("new_content = %q, want %q", got, want)
	}
}

func TestStrictPatchPromptUsesBaselineDiffWhenSessionAvailable(t *testing.T) {
	t.Setenv("MACHTIANI_SESSION_ID", "baseline-session")
	repoRoot := t.TempDir()
	relPath := "README.md"
	content := "Line one\nLine two\n"
	cmd := exec.Command("git", "init")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, prompt string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			return `{"path":"README.md","reason":"baseline diff"}`, nil
		case 2:
			if !strings.Contains(prompt, "Baseline-relative diff") {
				t.Fatalf("expected baseline diff preamble, got %q", prompt)
			}
			if !strings.Contains(prompt, "```diff") {
				t.Fatalf("expected diff fence in prompt, got %q", prompt)
			}
			return `{
  "metadata": {"description": "example"},
  "edits": [
    {
      "path": "README.md",
      "mode": "patch",
      "start_line": 2,
      "end_line": 2,
      "new_content": "Line two updated\n"
    }
  ]
}`, nil
		default:
			t.Fatalf("unexpected chat invocation %d", call)
			return "", nil
		}
	}

	if _, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 5, ""); err != nil {
		t.Fatalf("runStrictPatchFlow error: %v", err)
	}
	if call != 2 {
		t.Fatalf("expected 2 chat invocations, got %d", call)
	}
}

func TestStrictPatchPromptUsesBaselineDiffFromConfigWhenEnvMissing(t *testing.T) {
	t.Setenv("MACHTIANI_SESSION_ID", "")
	repoRoot := t.TempDir()
	relPath := "README.md"
	content := "Line one\nLine two\n"
	cmd := exec.Command("git", "init")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot, SessionID: "config-session"})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, prompt string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			return `{"path":"README.md","reason":"baseline diff"}`, nil
		case 2:
			if !strings.Contains(prompt, "Baseline-relative diff") {
				t.Fatalf("expected baseline diff preamble, got %q", prompt)
			}
			if !strings.Contains(prompt, "```diff") {
				t.Fatalf("expected diff fence in prompt, got %q", prompt)
			}
			return `{
  "metadata": {"description": "example"},
  "edits": [
    {
      "path": "README.md",
      "mode": "patch",
      "start_line": 2,
      "end_line": 2,
      "new_content": "Line two updated\n"
    }
  ]
}`, nil
		default:
			t.Fatalf("unexpected chat invocation %d", call)
		}
		return "", nil
	}

	if _, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 5, ""); err != nil {
		t.Fatalf("runStrictPatchFlow error: %v", err)
	}
	if call != 2 {
		t.Fatalf("expected 2 chat invocations, got %d", call)
	}
}

func TestStrictPatchFailsWhenBaselineCaptureFails(t *testing.T) {
	t.Setenv("MACHTIANI_SESSION_ID", "baseline-missing-session")
	repoRoot := t.TempDir()
	relPath := "README.md"
	content := "Line one\nLine two\n"
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, prompt string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		if call == 1 {
			return fmt.Sprintf(`{"path":"%s","reason":"missing baseline"}`, relPath), nil
		}
		t.Fatalf("unexpected chat invocation %d with prompt %q", call, prompt)
		return "", nil
	}

	if _, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 5, ""); err == nil {
		t.Fatalf("expected error when baseline capture fails")
	} else if !strings.Contains(err.Error(), "ensure baseline") {
		t.Fatalf("expected baseline ensure error, got %v", err)
	}
	if call != 1 {
		t.Fatalf("expected only path selection invocation, got %d", call)
	}
}

func TestStrictPatchNormalizationHandlesOldNewText(t *testing.T) {
	repoRoot := t.TempDir()
	relPath := "README.md"
	content := "Alpha\nBravo\nCharlie\n"
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, _ string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			return `{"path":"README.md","reason":"update"}`, nil
		case 2:
			return `{
	  "metadata": {"description": "Fix line"},
	  "edits": [
	    {
	      "path": "README.md",
	      "mode": "patch",
	      "start_line": 2,
	      "end_line": 2,
	      "new_text": "Bravo!!!\n"
	    }
	  ]
	}`, nil
		default:
			t.Fatalf("unexpected call %d", call)
			return "", nil
		}
	}

	payload, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 5, "")
	if err != nil {
		t.Fatalf("runStrictPatchFlow error: %v", err)
	}
	var instr patcher.Instructions
	if err := json.Unmarshal([]byte(payload), &instr); err != nil {
		t.Fatalf("decode returned payload: %v", err)
	}
	ed := instr.Edits[0]
	if got, want := ed.StartLine, 2; got != want {
		t.Fatalf("start_line = %d, want %d", got, want)
	}
	if got, want := ed.EndLine, 2; got != want {
		t.Fatalf("end_line = %d, want %d", got, want)
	}
	if got, want := ed.NewContent, "Bravo!!!\n"; got != want {
		t.Fatalf("new_content = %q, want %q", got, want)
	}
}

func TestStrictPatchRequestsFullReloadTriggersRetry(t *testing.T) {
	repoRoot := t.TempDir()
	relPath := "big.txt"
	var builder strings.Builder
	for i := 1; i <= strictPatchMaxFileLines+20; i++ {
		fmt.Fprintf(&builder, "Line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(builder.String()), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, prompt string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			return `{"path":"big.txt","reason":"Large file edit"}`, nil
		case 2:
			if !strings.Contains(prompt, "... (20 additional line(s) truncated)") {
				t.Fatalf("expected truncated prompt, got %q", prompt)
			}
			if strings.Contains(prompt, "801 | Line 801") {
				t.Fatalf("truncated prompt should not include line 801: %q", prompt)
			}
			return `{"request_full_reload": true}`, nil
		case 3:
			if strings.Contains(prompt, "... (20 additional line(s) truncated)") {
				t.Fatalf("full reload prompt should not be truncated: %q", prompt)
			}
			if !strings.Contains(prompt, "801 | Line 801") {
				t.Fatalf("full reload prompt missing extended context: %q", prompt)
			}
			return `{
	  "metadata": {"description": "Adjust tail line"},
	  "edits": [
	    {
	      "path": "big.txt",
	      "mode": "patch",
	      "start_line": 805,
	      "end_line": 805,
	      "new_content": "Line 805 updated\n"
	    }
	  ]
	}`, nil
		default:
			t.Fatalf("unexpected chat invocation %d", call)
			return "", nil
		}
	}

	payload, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 5, "")
	if err != nil {
		t.Fatalf("runStrictPatchFlow error: %v", err)
	}
	if call != 3 {
		t.Fatalf("expected 3 chat invocations, got %d", call)
	}

	var instr patcher.Instructions
	if err := json.Unmarshal([]byte(payload), &instr); err != nil {
		t.Fatalf("decode returned payload: %v", err)
	}
	if len(instr.Edits) != 1 {
		t.Fatalf("expected one edit, got %d", len(instr.Edits))
	}
	ed := instr.Edits[0]
	if got, want := ed.StartLine, 805; got != want {
		t.Fatalf("start_line = %d, want %d", got, want)
	}
	if got, want := ed.EndLine, 805; got != want {
		t.Fatalf("end_line = %d, want %d", got, want)
	}
	if got, want := ed.NewContent, "Line 805 updated\n"; got != want {
		t.Fatalf("new_content = %q, want %q", got, want)
	}
}

func TestPreValidateStrictPatchJSONEscapesControlCharacters(t *testing.T) {
	raw := []byte(`{
  "metadata": {},
  "edits": [
    {
      "path": "transcript.go",
      "mode": "patch",
      "start_line": 10,
      "end_line": 10,
      "new_content": "\treturn\thasTab\n"
    }
  ]
}`)

	// Inject literal tabs into the JSON to mimic LLM output
	raw = bytes.ReplaceAll(raw, []byte("\\t"), []byte("\t"))

	sanitized, err := preValidateStrictPatchJSON(raw, false)
	if err != nil {
		t.Fatalf("preValidateStrictPatchJSON returned error: %v", err)
	}

	if bytes.Contains(sanitized, []byte{'	'}) {
		t.Fatalf("expected tabs to be escaped, got %q", sanitized)
	}
	if !bytes.Contains(sanitized, []byte("\\t")) {
		t.Fatalf("expected escaped tab sequence in sanitized output: %q", sanitized)
	}

	var instr patcher.Instructions
	if err := json.Unmarshal(sanitized, &instr); err != nil {
		t.Fatalf("sanitized payload should unmarshal: %v", err)
	}
	if len(instr.Edits) != 1 {
		t.Fatalf("expected one edit, got %d", len(instr.Edits))
	}
}

func TestStrictPatchAdjustsSnippetSourceForContext(t *testing.T) {
	repoRoot := t.TempDir()
	relPath := "LICENSE"
	content := "Line1\nLine2\nLine3\nLine4\n"
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, _ string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			return `{"path":"LICENSE","reason":"update"}`, nil
	case 2:
		return `{
	  "metadata": {"description": "Adjust snippet"},
	  "edits": [
	    {
	      "path": "LICENSE",
	      "mode": "patch",
	      "start_line": 3,
	      "end_line": 4,
	      "new_content": "Line3 updated\nLine4 rewritten"
	    }
	  ]
	}`, nil
		default:
			t.Fatalf("unexpected call %d", call)
			return "", nil
		}
	}

	payload, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 5, "")
	if err != nil {
		t.Fatalf("runStrictPatchFlow error: %v", err)
	}
	if call != 2 {
		t.Fatalf("expected 2 LLM calls, got %d", call)
	}
	var instr patcher.Instructions
	if err := json.Unmarshal([]byte(payload), &instr); err != nil {
		t.Fatalf("decode returned payload: %v", err)
	}
	if len(instr.Edits) != 1 {
		t.Fatalf("expected one edit, got %d", len(instr.Edits))
	}
	ed := instr.Edits[0]
	if got, want := ed.StartLine, 3; got != want {
		t.Fatalf("start_line = %d, want %d", got, want)
	}
	if got, want := ed.EndLine, 4; got != want {
		t.Fatalf("end_line = %d, want %d", got, want)
	}
	if got, want := ed.NewContent, "Line3 updated\nLine4 rewritten"; got != want {
		t.Fatalf("new_content = %q, want %q", got, want)
	}
}

func TestStrictPatchRetriesOnEmptyChoices(t *testing.T) {
	repoRoot := t.TempDir()
	relPath := "LICENSE"
	content := "Alpha\nBeta\n"
	if err := os.WriteFile(filepath.Join(repoRoot, relPath), []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	client := NewClient(ClientConfig{RepoRoot: repoRoot})
	var mu sync.Mutex
	call := 0
	client.chatFn = func(_ context.Context, _ string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		call++
		switch call {
		case 1:
			return "", llm.ErrNoChoices
		case 2:
			return `{"path":"LICENSE","reason":"retry"}`, nil
		case 3:
			return "", llm.ErrNoChoices
		case 4:
			return `{
	  "metadata": {"description": "Retry patch"},
	  "edits": [
	    {
	      "path": "LICENSE",
	      "mode": "patch",
	      "start_line": 1,
	      "end_line": 1,
	      "new_content": "Alpha updated\n"
	    }
	  ]
	}`, nil
		default:
			t.Fatalf("unexpected call %d", call)
			return "", nil
		}
	}

	payload, err := client.runStrictPatchFlow(context.Background(), "goal", "transcript", 1, 5, "")
	if err != nil {
		t.Fatalf("runStrictPatchFlow error: %v", err)
	}
	if call != 4 {
		t.Fatalf("expected 4 LLM calls (retries included), got %d", call)
	}
	if payload == "" {
		t.Fatalf("expected non-empty payload after retries")
	}
}

func TestTrimStrictPatchTranscriptDropsLowValueTurns(t *testing.T) {
	transcript := strings.Join([]string{
		"= MCT-AGENT TRANSCRIPT",
		"",
		"Session: agent-123",
		"",
		"== GOAL:",
		"",
		"Demo goal.",
		"",
		"== TURN 0",
		"",
		"Question: Provide project background.",
		"",
		"=== ANSWER",
		"",
		"Background details that are not relevant.",
		"",
		"Planner decision: background",
		"",
		"== TURN 1",
		"",
		"Question: Show the LICENSE file contents.",
		"",
		"Retrieved File Paths:",
		"* LICENSE",
		"",
		"=== ANSWER",
		"",
		"**Content:**",
		"```",
		"line a",
		"line b",
		"```",
		"",
		"Planner decision: ask",
	}, "\n")

	got := trimStrictPatchTranscript(transcript)
	if strings.Contains(got, "TURN 0") {
		t.Fatalf("expected background turn to be removed, got %q", got)
	}
	if !strings.Contains(got, "=== ANSWER") {
		t.Fatalf("expected answer section to be present, got %q", got)
	}
	if !strings.Contains(got, "line a") {
		t.Fatalf("expected answer content to be preserved, got %q", got)
	}
	if !strings.Contains(got, "== TURN 1") {
		t.Fatalf("expected recent turn to remain, got %q", got)
	}
	if !strings.Contains(got, "Planner decision: ask") {
		t.Fatalf("expected planner decision to be preserved, got %q", got)
	}
}

func TestTrimStrictPatchTranscriptKeepsOnlyMostRecentTurn(t *testing.T) {
	transcript := strings.Join([]string{
		"== TURN 3",
		"",
		"Question: Follow-up.",
		"",
		"Planner decision: ask",
		"",
		"== TURN 4",
		"",
		"Question: Latest action.",
		"",
		"Planner decision: patch",
	}, "\n")

	got := trimStrictPatchTranscript(transcript)
	if strings.Contains(got, "TURN 3") {
		t.Fatalf("expected only most recent turn, got %q", got)
	}
	if !strings.Contains(got, "TURN 4") {
		t.Fatalf("expected last turn to remain, got %q", got)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
