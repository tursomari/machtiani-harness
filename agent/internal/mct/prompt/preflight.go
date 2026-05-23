package prompt

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

const (
	preflightSystemPrompt     = "You classify user requests for a developer assistant. Reply with exactly `content` when the assistant should retrieve repository file content or filepaths (e.g., Get file content of a file; Get file content of every file that has a sought keyword; Get instruction content from documentation). Reply with exactly `shell` when the assistant should use developer tools or shell commands (e.g., List filepaths in a directory; Get a list of files that has a sought keyword; Run tests or build the project). Do not add any other words."
	preflightQuestionTemplate = "User request:\n\n%s\n\nShould the assistant retrieve repository file content and filepaths? Reply with exactly `content` or `shell`."
	preflightFallbackPrompt   = "Do you want relevant file content and filepaths, or should the assistant run commands? Reply with 'content' or 'shell'."
)

var (
	chatWithResolvedFallback = llm.ChatWithResolvedFallback
)

// PreflightShellRouting runs a lightweight LLM check to decide whether the
// prompt should use shell-agent mode. It returns false when content-retrieval
// mode is appropriate (LLM replies "content"), true when shell-agent mode
// should be used (LLM replies "shell"). Errors and empty replies default to
// shell-agent to preserve existing behaviour.
func PreflightShellRouting(ctx context.Context, runtime ModelRuntime, prompt string) (bool, string, error) {
	trimmedPrompt := strings.TrimSpace(prompt)
	if shouldPreferContentRouting(trimmedPrompt) {
		return false, "content", nil
	}
	messages := []llm.Message{{Role: "system", Content: preflightSystemPrompt}}
	if trimmedPrompt != "" {
		question := fmt.Sprintf(preflightQuestionTemplate, trimmedPrompt)
		messages = append(messages, llm.Message{Role: "user", Content: question})
	} else {
		messages = append(messages, llm.Message{Role: "user", Content: preflightFallbackPrompt})
	}
	resolved := llm.CloneResolvedModel(runtime.Resolved)
	fallbackAliases := append([]string(nil), runtime.FallbackAliases...)
	fallbackResolved := cloneResolvedModels(runtime.FallbackResolved)
	extras := copyExtrasMap(runtime.Extras)

	resp, err := chatWithResolvedFallback(ctx, resolved, fallbackAliases, fallbackResolved, extras, messages)
	if err != nil {
		return true, "", err
	}

	trimmed := strings.TrimSpace(resp)
	if trimmed == "" {
		return true, "", nil
	}

	firstLine := trimmed
	if idx := strings.Index(firstLine, "\n"); idx != -1 {
		firstLine = firstLine[:idx]
	}
	firstLine = strings.TrimSpace(firstLine)
	normalized := strings.ToLower(firstLine)

	tokens := tokenizeForRouting(normalized)
	if len(tokens) == 0 {
		return true, firstLine, nil
	}

	if containsWord(tokens, "content") {
		return false, firstLine, nil
	}

	if containsWord(tokens, "shell") {
		return true, firstLine, nil
	}

	return true, firstLine, nil
}

func shouldPreferContentRouting(prompt string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(prompt))
	if trimmed == "" {
		return false
	}
	if containsRoutingPhrase(trimmed, []string{
		"run ", "execute ", "build ", "compile ", "start ", "restart ", "install ",
		"list filepaths", "list files", "current directory", "cwd",
		"shell command", "git status", "git diff", "grep ", "find ",
	}) {
		return false
	}
	hasExplainVerb := containsRoutingPhrase(trimmed, []string{
		"explain", "summarize", "describe", "what does", "how does", "where does", "what function", "which function", "what method", "which method", "identify the function", "identify the method", "outline",
	})
	hasCodeSignal := strings.Contains(trimmed, "`") || containsRoutingPhrase(trimmed, []string{
		".go", "function", "struct", "method", "symbol", "implementation", "logic", "code path",
		"definition", "defined", "fallback", "falls back", "model", "file discovery", "orchestrator",
		"line ", "lines ", "source", "code", "guardrail", "retry", "retries", "planner ask monitor", "flow",
	})
	return hasExplainVerb && hasCodeSignal
}

func containsRoutingPhrase(prompt string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(prompt, phrase) {
			return true
		}
	}
	return false
}

func tokenizeForRouting(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r)
	})
}

func containsWord(tokens []string, target string) bool {
	for _, tok := range tokens {
		if tok == target {
			return true
		}
	}
	return false
}

func containsSequence(tokens []string, sequence []string) bool {
	if len(sequence) == 0 || len(tokens) < len(sequence) {
		return false
	}
	for i := 0; i <= len(tokens)-len(sequence); i++ {
		match := true
		for j := range sequence {
			if tokens[i+j] != sequence[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
