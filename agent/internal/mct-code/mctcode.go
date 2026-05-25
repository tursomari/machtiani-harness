package mctcode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
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

	// 3. Fallback: parse XML-style <function-calls> blocks (e.g. from DeepSeek).
	if len(calls) == 0 {
		calls = parseXMLFunctionCalls(text)
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

// parseXMLFunctionCalls is a fallback parser for XML-style <function-calls>
// blocks that some models (e.g. DeepSeek) return instead of JSON. It extracts
// <invoke name="ToolName"> elements and their <parameter name="..."> children.
func parseXMLFunctionCalls(text string) []agents.ToolCall {
	var calls []agents.ToolCall

	startTag := "<function-calls>"
	endTag := "</function-calls>"
	remain := text

	for {
		start := strings.Index(remain, startTag)
		if start < 0 {
			break
		}
		start += len(startTag)

		end := strings.Index(remain[start:], endTag)
		if end < 0 {
			break
		}
		block := remain[start : start+end]
		remain = remain[start+end+len(endTag):]

		// Extract <invoke name="...">...</invoke> elements within this block.
		invokeTag := "<invoke "
		invokeEnd := "</invoke>"
		searchFrom := 0

		for {
			invStart := strings.Index(block[searchFrom:], invokeTag)
			if invStart < 0 {
				break
			}
			invStart += searchFrom

			invEnd := strings.Index(block[invStart:], invokeEnd)
			if invEnd < 0 {
				break
			}
			invBlock := block[invStart : invStart+invEnd+len(invokeEnd)]
			searchFrom = invStart + invEnd + len(invokeEnd)

			// Extract tool name from name="..."
			toolName := extractXMLAttr(invBlock, "name")
			if toolName == "" {
				continue
			}

			// Extract parameters.
			args := make(map[string]interface{})
			paramStart := "<parameter "
			paramEnd := "</parameter>"
			pSearch := 0

			for {
				pStart := strings.Index(invBlock[pSearch:], paramStart)
				if pStart < 0 {
					break
				}
				pStart += pSearch

				pEnd := strings.Index(invBlock[pStart:], paramEnd)
				if pEnd < 0 {
					break
				}
				pBlock := invBlock[pStart : pStart+pEnd+len(paramEnd)]
				pSearch = pStart + pEnd + len(paramEnd)

				paramName := extractXMLAttr(pBlock, "name")
				if paramName == "" {
					continue
				}

				// Extract value: everything between > and </parameter>
				valStart := strings.Index(pBlock, ">")
				if valStart < 0 {
					continue
				}
				valStart++

				valEnd := strings.LastIndex(pBlock, "</parameter>")
				if valEnd < 0 || valEnd <= valStart {
					continue
				}
				rawVal := strings.TrimSpace(pBlock[valStart:valEnd])

				// Attempt numeric / boolean conversion for usability downstream.
				args[paramName] = parseArgValue(rawVal)
			}

			calls = append(calls, agents.ToolCall{Tool: toolName, Args: args})
		}
	}

	return calls
}

// extractXMLAttr extracts the value of an attribute from an XML-like tag.
// It searches for name="value" and returns value (with quotes stripped).
func extractXMLAttr(tag, attrName string) string {
	needle := attrName + `="`
	idx := strings.Index(tag, needle)
	if idx < 0 {
		return ""
	}
	idx += len(needle)
	end := strings.Index(tag[idx:], `"`)
	if end < 0 {
		return ""
	}
	return tag[idx : idx+end]
}

// parseArgValue attempts to parse a string as a number or boolean, falling
// back to the raw string.
func parseArgValue(s string) interface{} {
	if s == "" {
		return s
	}
	// Try boolean first (short string, unambiguous).
	if b, err := strconv.ParseBool(s); err == nil {
		return b
	}
	// Try integer.
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return float64(i)
	}
	// Try float.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
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
	shellSvc := tools.NewShellService()

	// -- LLM client --
	client := &chatClient{modelAlias: modelAlias}

	// -- Agent --
	agent := agents.NewDefaultAgent(client, readSvc, writeSvc, patchSvc, multiPatchSvc, removeSvc, undoSvc, undoStack, searchSvc, shellSvc, verbose)

	answer, diffs, err := agent.Run(ctx, handoffNote)
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}

	fmt.Println(answer)
	if len(diffs) > 0 {
		fmt.Println()
		fmt.Println("Changes made:")
		for _, d := range diffs {
			fmt.Println(d)
			fmt.Println()
		}
	}
	return nil
}
