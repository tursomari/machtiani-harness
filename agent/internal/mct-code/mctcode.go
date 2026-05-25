package mctcode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct-code/internal/agents"
	"github.com/tursomari/machtiani/agent/internal/mct-code/internal/tools"
)

// ---------------------------------------------------------------------------
// LLM client wrapper
// ---------------------------------------------------------------------------

// chatClient adapts the raw llm.Chat function to the agents.LLMClient interface
// by parsing JSON tool-call blocks out of the response text.
type chatClient struct {
	modelAlias string
}

// ChatCompletion sends messages to the LLM, parses the response, and returns
// any tool calls found embedded in the text as JSON blocks.
func (c *chatClient) ChatCompletion(ctx context.Context, messages []llm.Message) (agents.ChatCompletionResponse, error) {
	text, err := llm.Chat(ctx, c.modelAlias, nil, messages)
	if err != nil {
		return agents.ChatCompletionResponse{}, fmt.Errorf("llm chat: %w", err)
	}

	toolCalls := parseToolCalls(text)
	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}

	return agents.ChatCompletionResponse{
		FinishReason: finishReason,
		Content:      text,
		ToolCalls:    toolCalls,
	}, nil
}

// parseToolCalls scans each line of text for JSON objects containing "tool" and
// "args" keys and returns them as ToolCall values.
//
// For each line it first tries to parse the whole line as JSON (the common case
// when the LLM emits a tool-call block on its own line). If that fails it falls
// back to scanning the line for {"tool": …} blocks embedded in other text.
func parseToolCalls(text string) []agents.ToolCall {
	var calls []agents.ToolCall
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 1. Try to parse the whole line as a single JSON object.
		var raw struct {
			Tool string                 `json:"tool"`
			Args map[string]interface{} `json:"args"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err == nil && raw.Tool != "" {
			calls = append(calls, agents.ToolCall{Tool: raw.Tool, Args: raw.Args})
			continue
		}

		// 2. Fallback: scan inside the line for {"tool": …} JSON objects.
		searchFrom := 0
		for {
			startIdx := findToolJSONStart(line, searchFrom)
			if startIdx < 0 {
				break
			}

			// Find the matching closing brace, handling nesting.
			depth := 1
			endIdx := startIdx + 1
			for endIdx < len(line) && depth > 0 {
				if line[endIdx] == '{' {
					depth++
				} else if line[endIdx] == '}' {
					depth--
				}
				if depth > 0 {
					endIdx++
				}
			}
			if depth != 0 {
				searchFrom = startIdx + 1
				continue
			}

			sub := line[startIdx : endIdx+1]
			if err := json.Unmarshal([]byte(sub), &raw); err != nil || raw.Tool == "" {
				searchFrom = startIdx + 1
				continue
			}
			calls = append(calls, agents.ToolCall{Tool: raw.Tool, Args: raw.Args})
			searchFrom = endIdx + 1
		}
	}
	return calls
}

// findToolJSONStart locates the next '{' that introduces a JSON object with
// "tool" as its first key. It allows optional whitespace between '{' and
// '"tool"' and between '"tool"' and ':'.
func findToolJSONStart(s string, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		// Skip whitespace after '{'
		j := i + 1
		for j < len(s) && s[j] == ' ' {
			j++
		}
		// Check for "tool"
		if !strings.HasPrefix(s[j:], `"tool"`) {
			continue
		}
		j += 6 // len(`"tool"`)
		// Skip whitespace before ':'
		for j < len(s) && s[j] == ' ' {
			j++
		}
		if j < len(s) && s[j] == ':' {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Run is the top-level entry point. It reads the handoff note from os.Args[1],
// wires up all services and the LLM client, creates a DefaultAgent, and runs it.
func Run(verbose bool) error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: mct-code <handoff-note>")
	}
	handoffNote := os.Args[1]

	ctx := context.Background()

	// Determine the model alias (env var or default).
	modelAlias := os.Getenv("MCT_DEFAULT_MODEL")
	if modelAlias == "" {
		modelAlias = "default"
	}

	// -- Services --
	undoStack := tools.NewUndoStack()
	readSvc := tools.NewFsReadService()
	writeSvc := tools.NewFsWriteService(readSvc)
	patchSvc := tools.NewFsPatchService(readSvc, undoStack)
	multiPatchSvc := tools.NewFsMultiPatchService(readSvc, undoStack)
	removeSvc := tools.NewFsRemoveService(readSvc, undoStack)
	undoSvc := tools.NewFSUndoService(undoStack)
	searchSvc := tools.NewFsSearchService()

	// -- LLM client --
	client := &chatClient{modelAlias: modelAlias}

	// -- Agent --
	agent := agents.NewDefaultAgent(client, readSvc, writeSvc, patchSvc, multiPatchSvc, removeSvc, undoSvc, undoStack, searchSvc, verbose)

	answer, err := agent.Run(ctx, handoffNote)
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}

	fmt.Println(answer)
	return nil
}
