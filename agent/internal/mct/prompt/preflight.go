package prompt

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

const (
	preflightSystemPrompt     = "You classify user requests for a developer assistant. Reply with exactly `yes` when the assistant should retrieve repository file content/filepaths. Reply with exactly `something else` when the assistant should use tools such as shell commands. Do not add any other words."
	preflightQuestionTemplate = "User request:\n\n%s\n\nShould the assistant retrieve repository file content and filepaths? Reply with exactly `yes` or `something else`."
	preflightFallbackPrompt   = "Do you want relevant file content and filepaths, or something else? Reply with 'yes' or 'something else'."
)

var (
	chatWithResolvedFallback = llm.ChatWithResolvedFallback
)

// PreflightShellRouting runs a lightweight LLM check to decide whether the
// prompt should use shell-agent mode. It returns true when shell-agent should
// be used, along with the raw LLM reply. Errors (including empty replies)
// default to shell-agent to preserve existing behaviour.
func PreflightShellRouting(ctx context.Context, runtime ModelRuntime, prompt string) (bool, string, error) {
	trimmedPrompt := strings.TrimSpace(prompt)
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

	if containsWord(tokens, "yes") {
		return false, firstLine, nil
	}

	if containsSequence(tokens, []string{"something", "else"}) {
		return true, firstLine, nil
	}

	return true, firstLine, nil
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
