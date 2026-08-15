package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/core/mctcode/internal/tools"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// ToolCall represents a single tool invocation parsed from the LLM response.
type ToolCall struct {
	Tool string
	Args map[string]interface{}
}

// ChatCompletionResponse is the structured response returned by an LLMClient.
type ChatCompletionResponse struct {
	FinishReason string
	Content      string
	ToolCalls    []ToolCall
}

// LLMClient is the interface the agent uses to chat with a language model.
type LLMClient interface {
	ChatCompletion(ctx context.Context, messages []llm.Message) (ChatCompletionResponse, error)
}

// ---------------------------------------------------------------------------
// DefaultAgent
// ---------------------------------------------------------------------------

// DefaultAgent implements a tool-using agent over the filesystem services.
type DefaultAgent struct {
	llmClient     LLMClient
	readSvc       *tools.FsReadService
	writeSvc      *tools.FsWriteService
	patchSvc      *tools.FsPatchService
	multiPatchSvc *tools.FsMultiPatchService
	removeSvc     *tools.FsRemoveService
	undoSvc       *tools.FSUndoService
	undoStack     *tools.UndoStack
	searchSvc     *tools.FsSearchService
	shellSvc      *tools.ShellService
	verbose       bool
	diffs         []string
	maxIterations int
	retryAttempts int
}

// NewDefaultAgent creates a DefaultAgent wired to the given LLM and services.
func NewDefaultAgent(
	llmClient LLMClient,
	readSvc *tools.FsReadService,
	writeSvc *tools.FsWriteService,
	patchSvc *tools.FsPatchService,
	multiPatchSvc *tools.FsMultiPatchService,
	removeSvc *tools.FsRemoveService,
	undoSvc *tools.FSUndoService,
	undoStack *tools.UndoStack,
	searchSvc *tools.FsSearchService,
	shellSvc *tools.ShellService,
	verbose bool,
	maxIterations int,
	retryAttempts int,
) *DefaultAgent {
	return &DefaultAgent{
		llmClient:     llmClient,
		readSvc:       readSvc,
		writeSvc:      writeSvc,
		patchSvc:      patchSvc,
		multiPatchSvc: multiPatchSvc,
		removeSvc:     removeSvc,
		undoSvc:       undoSvc,
		undoStack:     undoStack,
		searchSvc:     searchSvc,
		shellSvc:      shellSvc,
		verbose:       verbose,
		diffs:         []string{},
		maxIterations: maxIterations,
		retryAttempts: retryAttempts,
	}
}

// buildSystemPrompt returns the system message with JSON function-calling tool
// definitions for all six filesystem tools.
func (a *DefaultAgent) buildSystemPrompt() string {
	var b strings.Builder
	b.WriteString(`You are a coding assistant with access to filesystem tools.
The workspace is the current working directory. All file paths are relative to this directory unless specified as absolute. When you need to find files, use FSSearch to search the codebase with regex patterns. Use "." as the path to search the entire workspace.

Each tool call must be a single-line JSON object on its own line, for example {"tool":"FSRead","args":{"path":"lib/api/api-request.js"}}. Do not wrap the JSON in any XML tags, backticks, or code fences.

Available tools:
`)
	for _, t := range tools.AllTools() {
		schemaJSON, _ := json.Marshal(t.Schema)
		b.WriteString(fmt.Sprintf("\n%s\n   Description: %s\n   Schema: %s\n", t.Name, t.Description, string(schemaJSON)))
	}
	b.WriteString("\nRules:\n")
	b.WriteString("- Read a file before modifying it.\n")
	b.WriteString("- Each tool output must be on its own line.\n")
	b.WriteString("- CRITICAL: You MUST use FS tools (FSRead, FSPatch, FSWrite, FSSearch) for ALL file operations. Use FSRead instead of cat/head/tail. Use FSPatch or FSWrite instead of sed/awk/echo redirect. Use FSSearch instead of grep/find. Reserve Shell ONLY for git, npm, docker, and similar system commands. Shell must never be used for reading, searching, or modifying files. If you use Shell for any file operation, the task is considered failed.\n")
	b.WriteString("- For every file modification, follow this exact sequence: FSRead the file, then FSPatch to apply the edit, then FSRead again to verify the change was applied correctly.\n")
	b.WriteString("- You MUST process ALL files identified by your search before declaring the task complete. After each file, check your search results for the next unprocessed file and continue. If you stop before all files are done, the task is considered failed.\n")
	b.WriteString("- When you are done, respond with a summary of what you did.\n")
	b.WriteString("- When you make file edits, include a summary of what changed together with the diff output.\n")
	return b.String()
}

// Run executes the agent loop: send messages, receive tool calls, dispatch, repeat.
func (a *DefaultAgent) Run(ctx context.Context, handoffNote string) (string, []string, error) {
	messages := []llm.Message{
		{Role: "system", Content: a.buildSystemPrompt()},
		{Role: "user", Content: handoffNote},
	}

	iter := 0
	for {
		iter++
		if iter > a.maxIterations {
			if a.verbose {
				fmt.Fprintf(os.Stderr, "mct-code: max iterations (%d) reached\n", a.maxIterations)
			}
			return "", nil, fmt.Errorf("mct-code: max iterations (%d) reached", a.maxIterations)
		}

		resp, err := a.chatCompletionWithRetry(ctx, messages)
		if err != nil {
			return "", nil, fmt.Errorf("agent: chat error: %w", err)
		}

		// Append assistant response to message history.
		if resp.Content != "" {
			messages = append(messages, llm.Message{Role: "assistant", Content: resp.Content})
		}

		// No tool calls → check for inline <mct_tool_call> tags as fallback.
		if len(resp.ToolCalls) == 0 {
			if parsed := parseMctToolCallsFromContent(resp.Content); len(parsed) > 0 {
				resp.ToolCalls = parsed
			} else {
				return resp.Content, a.diffs, nil
			}
		}

		if a.verbose {
			for _, tc := range resp.ToolCalls {
				fmt.Fprintf(os.Stderr, "Tool: %s %v\n", tc.Tool, tc.Args)
			}
		}

		// Execute each tool call and append results.
		for _, tc := range resp.ToolCalls {
			tr := a.dispatch(ctx, tc)
			if a.verbose {
				if tr.Error != nil {
					fmt.Fprintf(os.Stderr, " -> error: %v\n", tr.Error)
				} else {
					fmt.Fprintf(os.Stderr, " -> success (%d bytes)\n", len(tr.Content))
				}
			}
			content := tr.Content
			if tr.Error != nil {
				content = fmt.Sprintf("error: %v", tr.Error)
			} else if tr.Before != "" && tr.After != "" && tr.Before != tr.After {
				d := tools.DiffString(tr.Before, tr.After)
				if d != "" {
					content = d + "\n\n" + tr.Content
					a.diffs = append(a.diffs, d)
				}
				if a.verbose && d != "" {
					for _, line := range strings.Split(d, "\n") {
						fmt.Fprintf(os.Stderr, "\t%s\n", line)
					}
				}
			}
			messages = append(messages, llm.Message{Role: "user", Content: content})
		}
	}
}

// parseMctToolCallsFromContent scans content for <mct_tool_call>...</mct_tool_call>
// and <mc_tool_call>...</mc_tool_call> tags, extracting ToolCall values
// from the JSON inside each tag.
func parseMctToolCallsFromContent(content string) []ToolCall {
	var calls []ToolCall
	calls = append(calls, extractToolCalls(content, "<mct_tool_call>", "</mct_tool_call>")...)
	calls = append(calls, extractToolCalls(content, "<mc_tool_call>", "</mc_tool_call>")...)
	return calls
}

// extractToolCalls is a helper that scans content for the given open/close tags
// and returns any ToolCall values found.
func extractToolCalls(content, openTag, closeTag string) []ToolCall {
	var calls []ToolCall
	pos := 0
	for {
		start := strings.Index(content[pos:], openTag)
		if start == -1 {
			break
		}
		start += pos + len(openTag)
		end := strings.Index(content[start:], closeTag)
		if end == -1 {
			break
		}
		jsonStr := content[start : start+end]
		var tc ToolCall
		if err := json.Unmarshal([]byte(jsonStr), &tc); err != nil {
			pos = start + end + len(closeTag)
			continue
		}
		if tc.Tool == "" {
			pos = start + end + len(closeTag)
			continue
		}
		calls = append(calls, tc)
		pos = start + end + len(closeTag)
	}
	return calls
}

// isTransientError returns true if the error is likely transient and retryable.
func isTransientError(err error) bool {
	transientSubstrings := []string{
		"timeout",
		"connection refused",
		"connection reset",
		"broken pipe",
		"HTTP 429",
		"HTTP 500",
		"HTTP 502",
		"HTTP 503",
		"HTTP 504",
		"rate limit",
		"throttle",
		"transient",
	}

	msg := err.Error()
	for _, substr := range transientSubstrings {
		if strings.Contains(msg, substr) {
			return true
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	return false
}

// chatCompletionWithRetry calls the LLM client with retry logic.
// It retries up to retryAttempts times with exponential backoff for transient errors.
func (a *DefaultAgent) chatCompletionWithRetry(ctx context.Context, messages []llm.Message) (ChatCompletionResponse, error) {
	var lastErr error
	for attempt := 0; attempt < a.retryAttempts; attempt++ {
		resp, err := a.llmClient.ChatCompletion(ctx, messages)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		if !isTransientError(err) {
			return ChatCompletionResponse{}, err
		}

		backoff := time.Duration(1<<uint(attempt)) * time.Second
		if a.verbose {
			fmt.Fprintf(os.Stderr, "mct-code: retrying (attempt %d/%d, backoff %v) due to: %v\n", attempt+1, a.retryAttempts, backoff, err)
		}
		time.Sleep(backoff)
	}
	return ChatCompletionResponse{}, lastErr
}

// dispatch routes a tool call to the correct service.
func (a *DefaultAgent) dispatch(ctx context.Context, tc ToolCall) tools.ToolResult {
	switch tc.Tool {
	case "FSRead":
		path, _ := tc.Args["path"].(string)
		offset, _ := tc.Args["offset"].(float64)
		limit, _ := tc.Args["limit"].(float64)
		result, err := a.readSvc.Execute(ctx, path, int(offset), int(limit))
		if err != nil {
			return tools.ToolResult{Error: err}
		}
		return result

	case "FSWrite":
		path, _ := tc.Args["path"].(string)
		content, _ := tc.Args["content"].(string)
		overwrite, _ := tc.Args["overwrite"].(bool)
		result, err := a.writeSvc.Execute(ctx, path, content, overwrite)
		if err != nil {
			return tools.ToolResult{Error: err}
		}
		return result

	case "FSPatch":
		path, _ := tc.Args["path"].(string)
		oldStr, _ := tc.Args["old_string"].(string)
		newStr, _ := tc.Args["new_string"].(string)
		replaceAll, _ := tc.Args["replace_all"].(bool)
		result, err := a.patchSvc.Execute(ctx, path, oldStr, newStr, replaceAll)
		if err != nil {
			return tools.ToolResult{Error: err}
		}
		return result

	case "FSMultiPatch":
		path, _ := tc.Args["path"].(string)
		patchesRaw, ok := tc.Args["patches"].([]interface{})
		if !ok {
			return tools.ToolResult{Error: fmt.Errorf("patches must be an array")}
		}
		patches := make([][2]string, 0, len(patchesRaw))
		for i, pRaw := range patchesRaw {
			p, ok := pRaw.(map[string]interface{})
			if !ok {
				return tools.ToolResult{Error: fmt.Errorf("patch %d: expected object", i)}
			}
			oldStr, _ := p["old_string"].(string)
			newStr, _ := p["new_string"].(string)
			patches = append(patches, [2]string{oldStr, newStr})
		}
		result, err := a.multiPatchSvc.Execute(ctx, path, patches)
		if err != nil {
			return tools.ToolResult{Error: err}
		}
		return result

	case "FSRemove":
		path, _ := tc.Args["path"].(string)
		result, err := a.removeSvc.Execute(ctx, path)
		if err != nil {
			return tools.ToolResult{Error: err}
		}
		return result

	case "FSUndo":
		path, _ := tc.Args["path"].(string)
		result, err := a.undoSvc.Execute(ctx, path)
		if err != nil {
			return tools.ToolResult{Error: err}
		}
		return result

	case "FSSearch":
		params := tc.Args
		result, err := a.searchSvc.Execute(ctx, params)
		if err != nil {
			return tools.ToolResult{Error: err}
		}
		return result

	case "Shell":
		command, _ := tc.Args["command"].(string)
		cwd, _ := tc.Args["cwd"].(string)
		return a.shellSvc.Execute(ctx, command, cwd)

	default:
		return tools.ToolResult{Error: fmt.Errorf("unknown tool: %s", tc.Tool)}
	}
}
