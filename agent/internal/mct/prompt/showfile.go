package prompt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

const (
	showFileSystemPrompt = "You detect whether a planner prompt is asking to show or display the full content of a specific repository file. Reply with ONLY valid JSON. If it is a request to show full file contents, reply as: {\"is_show_file_request\":true,\"filepath\":\"path/to/file\"}. The filepath must be a workspace-relative path. If not, reply as: {\"is_show_file_request\":false}."
	showFileQuestionTmpl = "Planner prompt:\n\n%s\n\nReturn the JSON now."
)

type ShowFileDetection struct {
	IsShowFileRequest bool   `json:"is_show_file_request"`
	Filepath          string `json:"filepath"`
}

func (s ShowFileDetection) normalize() ShowFileDetection {
	s.Filepath = strings.TrimSpace(s.Filepath)
	if !s.IsShowFileRequest {
		s.Filepath = ""
		return s
	}
	if s.Filepath == "" {
		s.IsShowFileRequest = false
	}
	return s
}

// DetectShowFileRequest runs an LLM classification pass to determine whether
// the planner is requesting that we show the full contents of a particular
// repository file.
func DetectShowFileRequest(ctx context.Context, runtime ModelRuntime, plannerPrompt string) (ShowFileDetection, string, error) {
	trimmedPrompt := strings.TrimSpace(plannerPrompt)
	if trimmedPrompt == "" {
		return ShowFileDetection{IsShowFileRequest: false}, "", nil
	}

	messages := []llm.Message{
		{Role: "system", Content: showFileSystemPrompt},
		{Role: "user", Content: fmt.Sprintf(showFileQuestionTmpl, trimmedPrompt)},
	}

	resolved := llm.CloneResolvedModel(runtime.Resolved)
	fallbackAliases := append([]string(nil), runtime.FallbackAliases...)
	fallbackResolved := cloneResolvedModels(runtime.FallbackResolved)
	extras := copyExtrasMap(runtime.Extras)

	resp, err := chatWithResolvedFallback(ctx, resolved, fallbackAliases, fallbackResolved, extras, messages)
	if err != nil {
		return ShowFileDetection{IsShowFileRequest: false}, "", err
	}

	raw := strings.TrimSpace(resp)
	if raw == "" {
		return ShowFileDetection{IsShowFileRequest: false}, "", nil
	}
	// Models sometimes wrap JSON in markdown code fences or include extra text.
	// Try to extract the first JSON object from the response.
	if strings.HasPrefix(raw, "```") {
		trimmed := strings.TrimSpace(raw)
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```JSON")
		trimmed = strings.TrimPrefix(trimmed, "```")
		trimmed = strings.TrimSuffix(trimmed, "```")
		raw = strings.TrimSpace(trimmed)
	}
	if i := strings.Index(raw, "{"); i >= 0 {
		if j := strings.LastIndex(raw, "}"); j > i {
			raw = raw[i : j+1]
		}
	}

	var det ShowFileDetection
	if jerr := json.Unmarshal([]byte(raw), &det); jerr != nil {
		return ShowFileDetection{IsShowFileRequest: false}, raw, jerr
	}
	return det.normalize(), raw, nil
}
