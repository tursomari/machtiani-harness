package discovery

import (
	"fmt"
	"strings"
)

const systemPromptTemplate = "You are a snippet-discovery micro-agent. Your only goal is to locate relevant line ranges inside the provided files for the given reason.\n\nContext\n- You operate in the repository at the current working directory (`cwd`).\n- You may only read from the allow-listed files provided in the user message.\n- Snippets are identified purely by line numbers; no syntax awareness is required.\n\nTools (return exactly one tool call per turn; no extra text)\n- show: display file contents with line numbers.\n  Usage:\n  <show>\n  path/to/file\n  another/file\n  </show>\n  - You can list multiple files, one per line.\n\nFinal output\n- When ready, output exactly one JSON object and nothing else.\n- JSON format:\n  {\n    \"path/to/file\": [\n      {\"start\": 10, \"end\": 25},\n      {\"start\": 40, \"end\": 44}\n    ],\n    \"another/file\": [\n      {\"start\": 3, \"end\": 8}\n    ]\n  }\n\nCritical rules\n- Never emit JSON in the same turn as a <show> tool call.\n- Use only paths from the allow-list.\n- Line numbers must be within file bounds.\n- Overlapping ranges are allowed; do not merge them.\n- Output must be valid JSON and nothing else.\n\nEnd of system prompt."

func buildSystemPrompt() string {
	return strings.ReplaceAll(systemPromptTemplate, "\n", "\\n")
}

func buildInitialUserMessage(reason string, filePaths []string, transcript string, truncated bool) string {
	b := &strings.Builder{}
	if strings.TrimSpace(reason) != "" {
		fmt.Fprintf(b, "Reason:\n%s\n\n", reason)
	}
	if len(filePaths) > 0 {
		b.WriteString("Allow-listed files:\n")
		for _, path := range filePaths {
			fmt.Fprintf(b, "- %s\n", path)
		}
		b.WriteString("\n")
	}
	transcript = strings.TrimSpace(transcript)
	if transcript != "" {
		if truncated {
			transcript += "\n[TRUNCATED]"
		}
		b.WriteString("Transcript:\n")
		b.WriteString(transcript)
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func buildToolCallMissingMessage() string {
	return "Please issue a <show> tool call or output the final JSON object."
}

func buildShowRejectMessage(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "Show request rejected: format must be <show> with one file path per line."
	}
	return fmt.Sprintf("Show request rejected: %s", reason)
}

func buildFinalizeRejectMessage(reason string) string {
	if strings.TrimSpace(reason) == "" {
		return "Final output rejected: output only the JSON mapping of file paths to line ranges."
	}
	return fmt.Sprintf("Final output rejected: %s", reason)
}

func buildForcedFinalizeMessage() string {
	return "Finalization required: output ONLY the JSON mapping of file paths to line ranges now. Do not call <show>."
}
