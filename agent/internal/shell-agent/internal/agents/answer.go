package agents

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

func (a *DefaultAgent) extractFinalAnswer(content string) (string, bool) {
	answer, err := parseXMLAnswer(content, a.RunConfig.AnswerTag)
	if err != nil {
		return "", false
	}
	return answer, true
}

// parseXMLAnswer extracts the body of a final-answer block from a model
// response. The tag name is parameterised so the same parser handles
// both the default "answer" tag and any user-supplied override
// (e.g. "answercode" from --answer-tag).
//
// The implementation honours the rules in PlanAnswerTag.md:
//
//   - Rule 4 (first open, last close): the body spans from the FIRST
//     opening tag to the LAST closing tag. Stray close tags in the
//     middle are tolerated.
//   - Rule 6 (no-close fail-safe): when the open tag is present but no
//     close tag is found, the body runs to the end of the response
//     instead of erroring. This makes the parser forgiving of
//     truncated final answers without breaking rule 9 (no tag at all,
//     which still errors).
//   - Rule 7 (multiple opens): only the first opening tag is treated
//     as the start of the block; subsequent opens are body content.
//   - Rule 10 (empty body): if the resolved body trims to an empty
//     string, the response is rejected with a format-error-friendly
//     message.
func parseXMLAnswer(content, tag string) (string, error) {
	trimmed := strings.TrimSpace(content)
	tag = strings.TrimSpace(tag)
	if tag == "" {
		tag = "answer"
	}
	if trimmed == "" {
		return "", fmt.Errorf("response must include a <%s>...</%s> block", tag, tag)
	}

	openTag := "<" + tag + ">"
	closeTag := "</" + tag + ">"

	start := strings.Index(trimmed, openTag)
	if start == -1 {
		return "", fmt.Errorf("response must include a <%s>...</%s> block", tag, tag)
	}

	innerStart := start + len(openTag)

	// Rule 4: first open, last close. Rule 6: no-close fail-safe —
	// when the close tag is absent the body extends to the end of the
	// response so a truncated final answer is still surfaced.
	bodyEnd := len(trimmed)
	if lastClose := strings.LastIndex(trimmed, closeTag); lastClose >= innerStart {
		bodyEnd = lastClose
	}

	body := strings.TrimSpace(trimmed[innerStart:bodyEnd])
	if body == "" {
		return "", fmt.Errorf("response must include non-empty content inside <%s>...</%s>", tag, tag)
	}
	return body, nil
}

func (a *DefaultAgent) hasFinished(command string, result minisweagent.ExecuteResult) minisweagent.AgentError {
	if submitted := a.checkForFinalMarkerFile(); submitted != nil {
		return submitted
	}
	return a.checkForExitCommand(command)
}

func (a *DefaultAgent) checkForFinalMarkerFile() minisweagent.AgentError {
	for _, path := range finalMarkerCandidates(a.RunConfig.SessionID, a.workingDirectory()) {
		info, err := os.Stat(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && a.RunConfig.Verbose {
				log.Printf("warning: failed to stat final marker file %s: %v", path, err)
			}
			continue
		}
		if info.IsDir() {
			continue
		}

		result := ""
		if data, readErr := os.ReadFile(path); readErr == nil {
			trimmed := strings.TrimSpace(string(data))
			if trimmed != "" {
				result = trimmed
			}
		} else if a.RunConfig.Verbose {
			log.Printf("warning: failed to read final marker file %s: %v", path, readErr)
		}
		if result == "" {
			if trimmed := strings.TrimSpace(a.State.lastNonEmptyOutput); trimmed != "" {
				result = trimmed
			}
		}
		if err := os.Remove(path); err != nil {
			if a.RunConfig.Verbose {
				log.Printf("warning: failed to remove final marker file %s: %v", path, err)
			}
		}
		return &minisweagent.Submitted{Result: result}
	}
	return nil
}

func (a *DefaultAgent) checkForExitCommand(command string) minisweagent.AgentError {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return nil
	}
	switch cmd {
	case "exit", "exit 0":
		if trimmed := strings.TrimSpace(a.State.lastNonEmptyOutput); trimmed != "" {
			return &minisweagent.Submitted{Result: trimmed}
		}
		return &minisweagent.Submitted{Result: ""}
	}
	return nil
}

func finalMarkerCandidates(sessionID, cwd string) []string {
	paths := make([]string, 0, 3)
	seen := make(map[string]struct{})
	add := func(path string) {
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	add(minisweagent.FinalMarkerPath(sessionID))
	if cwd != "" {
		add(filepath.Join(cwd, minisweagent.FinalMarkerFilename))
		add(filepath.Join(cwd, "tmp", minisweagent.FinalMarkerFilename))
	}
	return paths
}

const forceFinalizeTemplate = "Reply to the user now based on the conversation so far. You are near the step limit with {{.RemainingSteps}} step(s) remaining. The original task was: \"{{.Task}}\". Answer the user's current need rather than automatically turning this into a full session wrap-up. Use relevant prior observations when helpful. If the user is asking for a summary or wrap-up, provide it. If important uncertainty remains, mention it briefly and concretely. Do not make further work requests or ask for more shell work. Output exactly one <{{.AnswerTag}}>...</{{.AnswerTag}}> block and no <{{.CommandTag}}> block. Put the entire final answer inside the <{{.AnswerTag}}> tags. The answer content may be Markdown/plain text. Do not include final-answer content outside the tags. Present the answer as a short list of substantive claims. Prefix each substantive claim with a confidence label formatted exactly as \"Confidence: <0-100>% - \". Do not provide a single overall confidence score; instead, every material factual claim or inference in the answer must carry its own confidence score, lowered when evidence is indirect, incomplete, or uncertain. Do not output a shell command."

const finalizeReminderTemplate = `You were asked to finalize your answer but you emitted a <{{.CommandTag}}> block instead of an <{{.AnswerTag}}> block. An <{{.AnswerTag}}> block must contain a natural-language conclusion summarizing your findings, results, or reasoning — not a bash command, not command output, and not a <{{.CommandTag}}> block. Respond now with exactly one <{{.AnswerTag}}>...</{{.AnswerTag}}> block containing your final answer. Do not emit any more <{{.CommandTag}}> blocks.`

func (a *DefaultAgent) maybeForceFinalize() error {
	if a == nil || a.RunConfig == nil || a.RunConfig.MaxSteps <= 0 {
		return nil
	}
	remaining := a.RunConfig.MaxSteps - a.RunConfig.Model.NCalls()
	if remaining <= 0 {
		return nil
	}

	threshold := a.RunConfig.FinalizeRemainingSteps
	if threshold <= 0 {
		threshold = 1
	}
	if remaining > threshold || a.State.finalizeRequested {
		return nil
	}

	msg, err := a.renderTemplate(forceFinalizeTemplate, map[string]interface{}{
		"remaining_steps": remaining,
		"RemainingSteps":  remaining,
	})
	if err != nil {
		return err
	}
	a.addMessage("user", msg, nil)
	a.State.finalizeRequested = true
	return nil
}
