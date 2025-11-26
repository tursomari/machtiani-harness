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
      "patch": {
        "hunks": [
          {
            "old_start": 1,
            "old_count": 3,
            "new_start": 1,
            "new_count": 3,
            "context_before": ["Alpha"],
            "deletions": ["Beta"],
            "additions": ["Beta 2025"],
            "context_after": ["Gamma"],
            "snippet_source": {"start_line": 1, "end_line": 3}
          }
        ]
      }
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
	if edit.PatchInfo == nil || len(edit.PatchInfo.Hunks) != 1 {
		t.Fatalf("expected one hunk in patch info, got %#v", edit.PatchInfo)
	}
	h := edit.PatchInfo.Hunks[0]
	if h.SnippetSource == nil {
		t.Fatalf("expected snippet source populated")
	}
	if h.SnippetSource.StartLine != 1 || h.SnippetSource.EndLine != 3 {
		t.Fatalf("snippet source lines mismatch: %#v", h.SnippetSource)
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
			if !strings.Contains(prompt, "context_before") {
				t.Fatalf("expected simplified guidance, got %q", prompt)
			}
			return `{
	  "metadata": {"description": "Update LICENSE year"},
	  "edits": [
	    {
	      "path": "LICENSE",
	      "mode": "patch",
	      "patch": {
	        "hunks": [
	          {
	            "context_before": "MIT License\n",
	            "context_after": "\nPermission is hereby granted, free of charge, to any person obtaining a copy",
	            "snippet_source": {"start_line": 1, "end_line": 5},
	            "replacement": "MIT License\n\nCopyright (c) 2020-2025 Matteo Collina and Undici contributors\n"
	          }
	        ]
	      }
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
	if ed.PatchInfo == nil || len(ed.PatchInfo.Hunks) != 1 {
		t.Fatalf("expected single hunk, got %#v", ed.PatchInfo)
	}
	h := ed.PatchInfo.Hunks[0]
	if got, want := h.Deletions, []string{"Copyright (c) Matteo Collina and Undici contributors"}; !slicesEqual(got, want) {
		t.Fatalf("deletions = %#v, want %#v", got, want)
	}
	if got, want := h.Additions, []string{"Copyright (c) 2020-2025 Matteo Collina and Undici contributors"}; !slicesEqual(got, want) {
		t.Fatalf("additions = %#v, want %#v", got, want)
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
      "patch": {
        "hunks": [
          {
            "old_start": 1,
            "old_count": 1,
            "new_start": 1,
            "new_count": 1,
            "context_before": ["Line one"],
            "deletions": ["Line two"],
            "additions": ["Line two updated"],
            "context_after": [],
            "snippet_source": {"start_line": 1, "end_line": 1}
          }
        ]
      }
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
      "patch": {
        "hunks": [
          {
            "old_start": 1,
            "old_count": 1,
            "new_start": 1,
            "new_count": 1,
            "context_before": ["Line one"],
            "deletions": ["Line two"],
            "additions": ["Line two updated"],
            "context_after": [],
            "snippet_source": {"start_line": 1, "end_line": 1}
          }
        ]
      }
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
	      "patch": {
	        "hunks": [
	          {
	            "snippet_source": {"start_line": 2, "end_line": 2},
	            "old_text": "Bravo",
	            "new_text": "Bravo!!!"
	          }
	        ]
	      }
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
	h := instr.Edits[0].PatchInfo.Hunks[0]
	if got, want := h.Deletions, []string{"Bravo"}; !slicesEqual(got, want) {
		t.Fatalf("deletions = %#v, want %#v", got, want)
	}
	if got, want := h.Additions, []string{"Bravo!!!"}; !slicesEqual(got, want) {
		t.Fatalf("additions = %#v, want %#v", got, want)
	}
}

func TestNormalizeStrictHunkAllowsContentMismatch(t *testing.T) {
	fileLines := []string{"alpha", "beta", "gamma"}
	hunk := map[string]any{
		"context_before": []any{"alpha"},
		"deletions":      []any{"BETA"},
		"additions":      []any{"BETA"},
		"context_after":  []any{"gamma"},
		"snippet_source": map[string]any{"start_line": 1, "end_line": 3},
	}
	if err := normalizeStrictHunk(hunk, "foo.txt", fileLines); err != nil {
		t.Fatalf("normalizeStrictHunk returned error: %v", err)
	}
	snippet, ok := hunk["snippet_source"].(map[string]any)
	if !ok {
		t.Fatalf("snippet_source not normalized: %#v", hunk["snippet_source"])
	}
	if snippet["start_line"] != 1 || snippet["end_line"] != 3 {
		t.Fatalf("unexpected snippet range: %#v", snippet)
	}
	if got := hunk["deletions"].([]string); !slicesEqual(got, []string{"BETA"}) {
		t.Fatalf("deletions = %#v, want %#v", got, []string{"BETA"})
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
	      "patch": {
	        "hunks": [
	          {
	            "context_before": ["Line 804"],
	            "deletions": ["Line 805"],
	            "additions": ["Line 805 updated"],
	            "context_after": ["Line 806"],
	            "snippet_source": {"start_line": 805, "end_line": 806}
	          }
	        ]
	      }
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
	if ed.PatchInfo == nil || len(ed.PatchInfo.Hunks) != 1 {
		t.Fatalf("expected single hunk, got %#v", ed.PatchInfo)
	}
	h := ed.PatchInfo.Hunks[0]
	if h.SnippetSource == nil {
		t.Fatalf("snippet source must be populated")
	}
	if h.SnippetSource.StartLine != 803 || h.SnippetSource.EndLine != 805 {
		t.Fatalf("snippet source lines mismatch: %#v", h.SnippetSource)
	}
	if got, want := h.Deletions, []string{"Line 805"}; !slicesEqual(got, want) {
		t.Fatalf("deletions = %#v, want %#v", got, want)
	}
	if got, want := h.Additions, []string{"Line 805 updated"}; !slicesEqual(got, want) {
		t.Fatalf("additions = %#v, want %#v", got, want)
	}
}

func TestPreValidateStrictPatchJSONEscapesControlCharacters(t *testing.T) {
	raw := []byte(`{
  "metadata": {},
  "edits": [
    {
      "path": "transcript.go",
      "mode": "patch",
      "patch": {
        "hunks": [
          {
            "old_start": 1,
            "old_count": 1,
            "new_start": 1,
            "new_count": 1,
            "context_before": ["func demo() {"],
            "additions": ["\treturn\thasTab"],
            "deletions": [],
            "context_after": ["}"]
          }
        ]
      }
    }
  ]
}`)

	// Inject literal tabs into the JSON to mimic LLM output
	raw = bytes.ReplaceAll(raw, []byte("\\t"), []byte("\t"))

	sanitized, err := preValidateStrictPatchJSON(raw)
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

func TestNormalizeStrictHunkRealignsOutOfBoundsSnippetSource(t *testing.T) {
	fileLines := []string{"Alpha", "Beta", "Gamma", "Delta"}
	hunk := map[string]any{
		"context_before": []string{"Alpha"},
		"deletions":      []string{"Beta"},
		"additions":      []string{"Beta 2025"},
		"context_after":  []string{"Gamma"},
		"snippet_source": map[string]any{
			"start_line": 120,
			"end_line":   120,
		},
	}

	if err := normalizeStrictHunk(hunk, "docs/file.txt", fileLines); err != nil {
		t.Fatalf("normalizeStrictHunk returned error: %v", err)
	}

	snippetMap, ok := toStringMap(hunk["snippet_source"])
	if !ok {
		t.Fatalf("snippet_source not normalized to map: %#v", hunk["snippet_source"])
	}
	startLine, err := toPositiveInt(snippetMap["start_line"])
	if err != nil {
		t.Fatalf("start_line parse error: %v", err)
	}
	if startLine != 1 {
		t.Fatalf("expected start_line 1, got %d", startLine)
	}
	endLinePtr, err := toIntValue(snippetMap["end_line"])
	if err != nil || endLinePtr == nil {
		t.Fatalf("end_line parse error: %v", err)
	}
	if *endLinePtr != 3 {
		t.Fatalf("expected end_line 3, got %d", *endLinePtr)
	}

	deletions, ok := hunk["deletions"].([]string)
	if !ok {
		t.Fatalf("deletions not []string: %#v", hunk["deletions"])
	}
	if len(deletions) != 1 || deletions[0] != "Beta" {
		t.Fatalf("unexpected deletions: %#v", deletions)
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
	      "patch": {
	        "hunks": [
	          {
	            "old_start": 3,
	            "old_count": 2,
	            "new_start": 3,
	            "new_count": 2,
	            "context_before": ["Line1", "Line2"],
	            "deletions": ["Line3"],
	            "additions": ["Line3 updated"],
	            "context_after": ["Line4"],
	            "snippet_source": {"start_line": 3, "end_line": 4}
	          }
	        ]
	      }
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
	if ed.PatchInfo == nil || len(ed.PatchInfo.Hunks) != 1 {
		t.Fatalf("expected single hunk, got %#v", ed.PatchInfo)
	}
	h := ed.PatchInfo.Hunks[0]
	if h.SnippetSource == nil {
		t.Fatalf("expected snippet source populated")
	}
	if h.SnippetSource.StartLine != 1 {
		t.Fatalf("snippet start line = %d, want 1", h.SnippetSource.StartLine)
	}
	if h.SnippetSource.EndLine != 4 {
		t.Fatalf("snippet end line = %d, want 4", h.SnippetSource.EndLine)
	}
	if h.OldStart != 1 {
		t.Fatalf("old_start = %d, want 1", h.OldStart)
	}
	if h.OldCount != 4 {
		t.Fatalf("old_count = %d, want 4", h.OldCount)
	}
	if h.NewCount != 4 {
		t.Fatalf("new_count = %d, want 4", h.NewCount)
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
	      "patch": {
	        "hunks": [
	          {
	            "old_start": 1,
	            "old_count": 2,
	            "new_start": 1,
	            "new_count": 2,
	            "context_before": [],
	            "deletions": ["Alpha"],
	            "additions": ["Alpha updated"],
	            "context_after": ["Beta"],
	            "snippet_source": {"start_line": 1, "end_line": 2}
	          }
	        ]
	      }
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
