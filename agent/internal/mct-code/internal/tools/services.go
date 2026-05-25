package tools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ToolResult is returned by every service Execute method.
type ToolResult struct {
	Content string
	Error   error
}

// ---------------------------------------------------------------------------
// UndoStack — per-path LIFO of file contents protected by a mutex.
// ---------------------------------------------------------------------------

// UndoStack holds saved file contents so FSUndo can restore them.
type UndoStack struct {
	mu    sync.Mutex
	stack map[string][]string
}

// NewUndoStack creates an empty UndoStack.
func NewUndoStack() *UndoStack {
	return &UndoStack{stack: make(map[string][]string)}
}

// Push saves content for the given path.
func (u *UndoStack) Push(path, content string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.stack[path] = append(u.stack[path], content)
}

// Pop restores the most recently saved content for the given path.
// Returns false when there is nothing to undo.
func (u *UndoStack) Pop(path string) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.stack[path]
	if len(s) == 0 {
		return "", false
	}
	content := s[len(s)-1]
	u.stack[path] = s[:len(s)-1]
	return content, true
}

// ---------------------------------------------------------------------------
// FsReadService
// ---------------------------------------------------------------------------

// FsReadService reads files and tracks which paths have been read.
type FsReadService struct {
	readFiles map[string]bool
	mu        sync.Mutex
}

// NewFsReadService returns a ready-to-use FsReadService.
func NewFsReadService() *FsReadService {
	return &FsReadService{readFiles: make(map[string]bool)}
}

// HasRead reports whether the given path has been read before.
func (s *FsReadService) HasRead(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readFiles[path]
}

// MarkRead records that the given path has been read (used by sibling services
// after a successful mutation so the file is considered "read" going forward).
func (s *FsReadService) MarkRead(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readFiles[path] = true
}

// Execute reads a file with an optional line offset and limit.
func (s *FsReadService) Execute(_ context.Context, path string, offset, limit int) (ToolResult, error) {
	s.mu.Lock()
	s.readFiles[path] = true
	s.mu.Unlock()

	file, err := os.Open(path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("read error: %w", err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	lineNum := 0
	for scanner.Scan() {
		if lineNum >= offset && (limit == 0 || lineNum < offset+limit) {
			lines = append(lines, fmt.Sprintf("%d: %s", lineNum, scanner.Text()))
		}
		lineNum++
		if limit > 0 && lineNum >= offset+limit {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return ToolResult{}, fmt.Errorf("scan error: %w", err)
	}
	return ToolResult{Content: strings.Join(lines, "\n")}, nil
}

// ---------------------------------------------------------------------------
// FsWriteService
// ---------------------------------------------------------------------------

// FsWriteService writes content to a file, enforcing read-before-write.
type FsWriteService struct {
	readSvc *FsReadService
	mu      sync.Mutex
}

// NewFsWriteService creates an FsWriteService that checks reads against the
// given FsReadService.
func NewFsWriteService(readSvc *FsReadService) *FsWriteService {
	return &FsWriteService{readSvc: readSvc}
}

// Execute writes content to path. It denies the write if the file exists and
// has not been read first. Set overwrite to true to allow replacing an existing
// file.
func (s *FsWriteService) Execute(_ context.Context, path, content string, overwrite bool) (ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Enforce read-before-write for existing files.
	if _, err := os.Stat(path); err == nil && !s.readSvc.HasRead(path) {
		return ToolResult{}, fmt.Errorf("write denied: file %s has not been read", path)
	}

	// Refuse to overwrite without explicit consent.
	if _, err := os.Stat(path); err == nil && !overwrite {
		return ToolResult{}, fmt.Errorf("write denied: %s exists and overwrite is false", path)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return ToolResult{}, fmt.Errorf("mkdir error: %w", err)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return ToolResult{}, fmt.Errorf("write error: %w", err)
	}

	s.readSvc.MarkRead(path)
	return ToolResult{Content: fmt.Sprintf("wrote %d bytes to %s", len(content), path)}, nil
}

// ---------------------------------------------------------------------------
// FsPatchService
// ---------------------------------------------------------------------------

// FsPatchService applies a single find-and-replace patch, requiring a prior
// read and enforcing uniqueness when replaceAll is false.
type FsPatchService struct {
	readSvc *FsReadService
	undo    *UndoStack
	mu      sync.Mutex
}

// NewFsPatchService creates an FsPatchService.
func NewFsPatchService(readSvc *FsReadService, undo *UndoStack) *FsPatchService {
	return &FsPatchService{readSvc: readSvc, undo: undo}
}

// Execute performs a find-and-replace on the file at path. When replaceAll is
// false, old_string must appear exactly once.
func (s *FsPatchService) Execute(_ context.Context, path, old, new string, replaceAll bool) (ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.readSvc.HasRead(path) {
		return ToolResult{}, fmt.Errorf("patch denied: file %s has not been read", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("patch read error: %w", err)
	}
	content := string(data)

	s.undo.Push(path, content)

	if !replaceAll {
		count := strings.Count(content, old)
		if count == 0 {
			return ToolResult{}, fmt.Errorf("patch error: old_string not found in %s", path)
		}
		if count > 1 {
			return ToolResult{}, fmt.Errorf("patch error: old_string appears %d times in %s; set replace_all=true", count, path)
		}
		content = strings.Replace(content, old, new, 1)
	} else {
		content = strings.ReplaceAll(content, old, new)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return ToolResult{}, fmt.Errorf("patch write error: %w", err)
	}

	s.readSvc.MarkRead(path)
	return ToolResult{Content: fmt.Sprintf("patched %s", path)}, nil
}

// ---------------------------------------------------------------------------
// FsMultiPatchService
// ---------------------------------------------------------------------------

// FsMultiPatchService applies multiple patches sequentially on an in-memory
// snapshot and writes back atomically. All patches must succeed or none are
// applied.
type FsMultiPatchService struct {
	readSvc *FsReadService
	undo    *UndoStack
	mu      sync.Mutex
}

// NewFsMultiPatchService creates an FsMultiPatchService.
func NewFsMultiPatchService(readSvc *FsReadService, undo *UndoStack) *FsMultiPatchService {
	return &FsMultiPatchService{readSvc: readSvc, undo: undo}
}

// Execute applies every patch in order to an in-memory copy, then writes the
// result back. If any patch fails the whole operation is aborted.
func (s *FsMultiPatchService) Execute(_ context.Context, path string, patches [][2]string) (ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.readSvc.HasRead(path) {
		return ToolResult{}, fmt.Errorf("multi-patch denied: file %s has not been read", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("multi-patch read error: %w", err)
	}
	snapshot := string(data)

	s.undo.Push(path, snapshot)

	for i, p := range patches {
		old, newStr := p[0], p[1]
		if !strings.Contains(snapshot, old) {
			return ToolResult{}, fmt.Errorf("multi-patch error: patch %d: old_string %q not found in %s", i, old, path)
		}
		snapshot = strings.Replace(snapshot, old, newStr, 1)
	}

	if err := os.WriteFile(path, []byte(snapshot), 0644); err != nil {
		return ToolResult{}, fmt.Errorf("multi-patch write error: %w", err)
	}

	s.readSvc.MarkRead(path)
	return ToolResult{Content: fmt.Sprintf("applied %d patches to %s", len(patches), path)}, nil
}

// ---------------------------------------------------------------------------
// FsRemoveService
// ---------------------------------------------------------------------------

// FsRemoveService deletes a file after pushing its contents onto an UndoStack.
type FsRemoveService struct {
	readSvc *FsReadService
	undo    *UndoStack
	mu      sync.Mutex
}

// NewFsRemoveService creates an FsRemoveService.
func NewFsRemoveService(readSvc *FsReadService, undo *UndoStack) *FsRemoveService {
	return &FsRemoveService{readSvc: readSvc, undo: undo}
}

// Execute reads file content, pushes it onto the undo stack, then deletes the
// file. Requires a prior read.
func (s *FsRemoveService) Execute(_ context.Context, path string) (ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.readSvc.HasRead(path) {
		return ToolResult{}, fmt.Errorf("remove denied: file %s has not been read", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("remove read error: %w", err)
	}

	s.undo.Push(path, string(data))

	if err := os.Remove(path); err != nil {
		return ToolResult{}, fmt.Errorf("remove error: %w", err)
	}

	return ToolResult{Content: fmt.Sprintf("removed %s", path)}, nil
}

// ---------------------------------------------------------------------------
// FSUndoService
// ---------------------------------------------------------------------------

// FSUndoService restores a file from the undo stack.
type FSUndoService struct {
	undo *UndoStack
	mu   sync.Mutex
}

// NewFSUndoService creates an FSUndoService.
func NewFSUndoService(undo *UndoStack) *FSUndoService {
	return &FSUndoService{undo: undo}
}

// Execute pops the most recent saved content for path and writes it back.
func (s *FSUndoService) Execute(_ context.Context, path string) (ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	content, ok := s.undo.Pop(path)
	if !ok {
		return ToolResult{}, fmt.Errorf("nothing to undo for %s", path)
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return ToolResult{}, fmt.Errorf("undo write error: %w", err)
	}

	return ToolResult{Content: fmt.Sprintf("restored %s (%d bytes)", path, len(content))}, nil
}

// ---------------------------------------------------------------------------
// FsSearchService
// ---------------------------------------------------------------------------

// FsSearchService searches file contents by shelling out to ripgrep (rg).
type FsSearchService struct {
	mu sync.Mutex
}

// NewFsSearchService returns a ready-to-use FsSearchService.
func NewFsSearchService() *FsSearchService {
	return &FsSearchService{}
}

// Execute runs rg with the given parameters and returns the output.
func (s *FsSearchService) Execute(_ context.Context, params map[string]interface{}) (ToolResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	patternRaw, ok := params["pattern"]
	if !ok {
		return ToolResult{}, fmt.Errorf("pattern is required")
	}
	pattern, ok := patternRaw.(string)
	if !ok {
		return ToolResult{}, fmt.Errorf("pattern must be a string")
	}

	var args []string

	// path (optional, defaults to ".")
	path := "."
	if pathRaw, ok := params["path"]; ok {
		if pathStr, ok := pathRaw.(string); ok {
			path = pathStr
		}
	}

	// glob (optional)
	if globRaw, ok := params["glob"]; ok {
		if globStr, ok := globRaw.(string); ok {
			args = append(args, "--glob", globStr)
		}
	}

	// file_type (optional)
	if ftRaw, ok := params["file_type"]; ok {
		if ftStr, ok := ftRaw.(string); ok {
			args = append(args, "--type", ftStr)
		}
	}

	// output_mode (optional, defaults to "files_with_matches")
	outputMode := "files_with_matches"
	if omRaw, ok := params["output_mode"]; ok {
		if omStr, ok := omRaw.(string); ok {
			outputMode = omStr
		}
	}
	switch outputMode {
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count")
	}

	// case_insensitive (optional, maps to -i)
	if ciRaw, ok := params["case_insensitive"]; ok {
		if ciBool, ok := ciRaw.(bool); ok && ciBool {
			args = append(args, "-i")
		}
	}

	// show_line_numbers (optional bool, maps to -n)
	if slnRaw, ok := params["show_line_numbers"]; ok {
		if slnBool, ok := slnRaw.(bool); ok && slnBool {
			args = append(args, "-n")
		}
	}

	// before_context (optional int, maps to -B)
	if bcRaw, ok := params["before_context"]; ok {
		if bcFloat, ok := bcRaw.(float64); ok {
			args = append(args, "-B", fmt.Sprintf("%d", int(bcFloat)))
		}
	}

	// after_context (optional int, maps to -A)
	if acRaw, ok := params["after_context"]; ok {
		if acFloat, ok := acRaw.(float64); ok {
			args = append(args, "-A", fmt.Sprintf("%d", int(acFloat)))
		}
	}

	// context (optional int, maps to -C)
	if cRaw, ok := params["context"]; ok {
		if cFloat, ok := cRaw.(float64); ok {
			args = append(args, "-C", fmt.Sprintf("%d", int(cFloat)))
		}
	}

	// head_limit (optional int)
	var headLimit int
	if hlRaw, ok := params["head_limit"]; ok {
		if hlFloat, ok := hlRaw.(float64); ok {
			headLimit = int(hlFloat)
		}
	}

	args = append(args, pattern, path)

	cmd := exec.Command("rg", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// rg exits with 1 when no matches are found — that is not an error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return ToolResult{Content: ""}, nil
		}
		return ToolResult{}, fmt.Errorf("rg error: %w\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if headLimit > 0 {
		lines := strings.Split(output, "\n")
		if len(lines) > headLimit {
			output = strings.Join(lines[:headLimit], "\n")
		}
	}

	return ToolResult{Content: output}, nil
}
