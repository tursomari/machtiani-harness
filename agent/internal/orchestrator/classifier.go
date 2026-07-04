package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/llm"
)

// ClassificationResult represents the outcome of classifying a final answer.
type ClassificationResult string

const (
	ClassificationRealSolution    ClassificationResult = "SOLUTION"
	ClassificationNotRealSolution ClassificationResult = "PERMISSION_ASK"
	ClassificationHardBlocker     ClassificationResult = "HARD_BLOCKER"
)

const classificationSystemPrompt = `You are a strict classifier. Analyze the following text and classify it into EXACTLY ONE of three categories.

You must respond with a structured output containing two fields:

CLASSIFICATION: <SOLUTION|PERMISSION_ASK|HARD_BLOCKER>
CONTINUATION: <text or empty>

Definitions:

SOLUTION — The content presents a genuinely completed solution: concrete code changes, implemented work, or a fully executed plan with specific deliverables. The work has been done and the answer describes what was accomplished. When classification is SOLUTION, leave the CONTINUATION field empty.

PERMISSION_ASK — The content is a permission ask, status report, "shall I proceed?", design discussion, recommendation without action, question about next steps, or anything short of an actual implemented solution. This includes planning, analysis, proposals, and status updates where concrete implementation has not yet occurred. When classification is PERMISSION_ASK, the CONTINUATION field MUST contain an authoritative instruction that commands mct-agent to continue working, implement the solution fully, and not ask for permission again. The continuation must be firm and directive, not a suggestion.

HARD_BLOCKER — The content describes an actual hard blocker that makes further continuation impossible, such as "no API credits available", a critical missing dependency that cannot be resolved, or an irrecoverable environment failure. When classification is HARD_BLOCKER, leave the CONTINUATION field empty.

Respond ONLY with the two lines in the exact format:
CLASSIFICATION: <value>
CONTINUATION: <text or empty>`

// ClassifyFinalAnswer reads the agent's final answer file for the given session
// and uses an LLM to classify it as a real solution, a permission ask, or a
// hard blocker.
func ClassifyFinalAnswer(ctx context.Context, sessionID string, modelAlias string) (ClassificationResult, string, error) {
	answerPath := filepath.Join(".machtiani", "sessions", sessionID, "chat", "agent-final-answer.md")

	data, err := os.ReadFile(answerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", fmt.Errorf("final answer file not found at %s: %w", answerPath, err)
		}
		return "", "", fmt.Errorf("reading final answer file %s: %w", answerPath, err)
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", "", fmt.Errorf("final answer file %s is empty", answerPath)
	}

	messages := []llm.Message{
		{
			Role:    "system",
			Content: classificationSystemPrompt,
		},
		{
			Role:    "user",
			Content: content,
		},
	}

	response, err := llm.Chat(ctx, modelAlias, nil, messages)
	if err != nil {
		return "", "", fmt.Errorf("LLM classification call failed: %w", err)
	}

	// Parse the structured response
	lines := strings.Split(response, "\n")
	var classificationToken string
	var continuationPrompt string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "CLASSIFICATION:") {
			classificationToken = strings.TrimSpace(strings.TrimPrefix(line, "CLASSIFICATION:"))
		} else if strings.HasPrefix(line, "CONTINUATION:") {
			continuationPrompt = strings.TrimSpace(strings.TrimPrefix(line, "CONTINUATION:"))
		}
	}

	if classificationToken == "" {
		return "", "", fmt.Errorf("unexpected classification response: missing CLASSIFICATION line; raw response: %s", response)
	}

	switch classificationToken {
	case "SOLUTION":
		return ClassificationRealSolution, "", nil
	case "PERMISSION_ASK":
		return ClassificationNotRealSolution, continuationPrompt, nil
	case "HARD_BLOCKER":
		return ClassificationHardBlocker, "", nil
	default:
		return "", "", fmt.Errorf("unexpected classification token %q; expected one of SOLUTION, PERMISSION_ASK, or HARD_BLOCKER; raw response: %s", classificationToken, response)
	}
}
