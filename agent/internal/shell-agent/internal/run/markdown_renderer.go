package run

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/shell-agent/pkg/minisweagent"
)

const (
	maxOutputLines = 10
	maxOutputChars = 2000
)

type stepSummary struct {
	Number   int
	Title    string
	Intent   string
	Thought  string
	Command  string
	ExitCode *int
	Success  *bool
	Error    string
	Output   string
}

type runRecord struct {
	Intent   string          `json:"intent"`
	Attempts []attemptRecord `json:"attempts"`
	Status   string          `json:"status"`
}

type attemptRecord struct {
	Command    string `json:"command"`
	ReturnCode int    `json:"return_code"`
	Output     string `json:"output"`
	Error      string `json:"error"`
}

// RenderCleanTranscript renders a human-friendly Markdown transcript summarizing the key
// steps from a recorded trajectory while omitting low-level retry and parser noise.
func RenderCleanTranscript(traj FileTrajectory) string {
	steps := buildStepSummaries(traj)
	if len(steps) == 0 {
		return "No executable steps were recorded."
	}

	var b strings.Builder
	for idx, step := range steps {
		if idx > 0 {
			b.WriteString("\n---\n\n")
		}
		heading := fmt.Sprintf("## Step %d", step.Number)
		if step.Title != "" {
			heading = fmt.Sprintf("%s: %s", heading, step.Title)
		}
		b.WriteString(heading)
		b.WriteString("\n")

		if step.Thought != "" {
			b.WriteString("**Thought:** ")
			b.WriteString(step.Thought)
			b.WriteString("\n\n")
		}

		if step.Command != "" {
			b.WriteString("**Action:**\n")
			b.WriteString("```bash\n")
			b.WriteString(step.Command)
			b.WriteString("\n```\n\n")
		}

		if step.Success != nil {
			if *step.Success {
				if step.ExitCode != nil {
					b.WriteString(fmt.Sprintf("**Result:** Success (exit code %d)\n", *step.ExitCode))
				} else {
					b.WriteString("**Result:** Success\n")
				}
			} else {
				summary := step.Error
				if summary == "" {
					summary = "command failed"
				}
				if step.ExitCode != nil {
					summary = fmt.Sprintf("%s (exit code %d)", summary, *step.ExitCode)
				}
				b.WriteString("**Result:** Failed: ")
				b.WriteString(summary)
				b.WriteString("\n")
			}
			b.WriteString("\n")
		} else if step.Error != "" {
			b.WriteString("**Result:** ")
			b.WriteString(step.Error)
			b.WriteString("\n\n")
		}

		if step.Output != "" {
			b.WriteString("**Output:**\n")
			b.WriteString("```\n")
			b.WriteString(step.Output)
			b.WriteString("\n```\n")
		}
	}

	return strings.TrimSpace(b.String())
}

// RenderSimpleTranscript renders a simplified transcript showing natural language intents
// and execution results in a clean format suitable for end users.
func RenderSimpleTranscript(traj FileTrajectory, configuredGlyphs ...presentation.GlyphSet) string {
	glyphs := presentation.GlyphsForMode(presentation.GlyphUnicode)
	if len(configuredGlyphs) > 0 {
		glyphs = configuredGlyphs[0]
	}
	if result := strings.TrimSpace(traj.Result); result != "" && !strings.EqualFold(traj.ExitStatus, "Error") {
		return result
	}
	steps := buildStepSummaries(traj)
	if len(steps) == 0 {
		return "No executable steps were recorded."
	}

	var b strings.Builder
	for idx, step := range steps {
		if idx > 0 {
			b.WriteString("\n")
		}

		b.WriteString("[BEGIN EXECUTION]\n\n")

		intent := strings.TrimSpace(step.Intent)
		if intent == "" {
			intent = strings.TrimSpace(step.Title)
		}
		if intent == "" {
			intent = "Execute command"
		}
		b.WriteString(intent)
		b.WriteString("\n")

		if step.Success != nil && *step.Success {
			b.WriteString(glyphs.Success + " Success")
		} else {
			errorMsg := strings.TrimSpace(step.Error)
			if step.Success != nil && !*step.Success {
				if errorMsg == "" && step.ExitCode != nil {
					errorMsg = fmt.Sprintf("Failed (exit code %d)", *step.ExitCode)
				}
			}
			if errorMsg == "" && step.ExitCode != nil {
				errorMsg = fmt.Sprintf("Failed (exit code %d)", *step.ExitCode)
			}
			if errorMsg == "" {
				errorMsg = "Failed"
			} else {
				lower := strings.ToLower(errorMsg)
				if !strings.Contains(lower, "failed") && !strings.Contains(lower, "error") && !strings.Contains(lower, "exit") {
					errorMsg = "Error: " + errorMsg
				}
			}
			b.WriteString(glyphs.Failure + " " + errorMsg)
		}
		b.WriteString("\n")

		if step.Output != "" {
			trimmedOutput := strings.TrimRight(step.Output, "\n")
			if trimmedOutput != "" {
				b.WriteString(trimmedOutput)
				b.WriteString("\n")
			}
		}

		b.WriteString("\n[END EXECUTION]")
	}

	return strings.TrimSpace(b.String())
}

func buildStepSummaries(traj FileTrajectory) []stepSummary {
	records := extractRunRecords(traj.ExtraInfo)
	steps := make([]stepSummary, 0, len(records))

	runByIntent := groupRecordsByIntent(records)
	stepNum := 0

	for idx, msg := range traj.Messages {
		if msg.Role != "assistant" {
			continue
		}
		intent := strings.TrimSpace(msg.Content)
		if intent == "" {
			continue
		}

		record, found := popNextRecord(runByIntent, intent)
		if !found {
			continue
		}

		stepNum++
		thought := strings.TrimSpace(msg.Content)
		intentText := strings.TrimSpace(record.Intent)
		summary := stepSummary{Number: stepNum, Title: deriveStepTitle(thought), Intent: intentText, Thought: thought}

		lastAttempt := lastAttempt(record.Attempts)
		if lastAttempt != nil {
			cmd := strings.TrimSpace(lastAttempt.Command)
			summary.Command = cmd
			if cmd != "" || lastAttempt.ReturnCode != 0 {
				exitCode := lastAttempt.ReturnCode
				summary.ExitCode = &exitCode
			}
			summary.Output = summarizeOutput(pickOutput(traj.Messages, idx, lastAttempt))
			if lastAttempt.Error != "" {
				summary.Error = strings.TrimSpace(lastAttempt.Error)
			}
		} else {
			summary.Output = summarizeOutput(pickOutput(traj.Messages, idx, nil))
		}

		success := strings.EqualFold(record.Status, "success")
		summary.Success = &success
		if !success && summary.Error == "" {
			summary.Error = deriveFailureSummary(record)
		}

		steps = append(steps, summary)
	}

	return steps
}

func extractRunRecords(extra map[string]interface{}) []runRecord {
	if extra == nil {
		return nil
	}
	raw, ok := extra["subprocess_runs"]
	if !ok || raw == nil {
		return nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var records []runRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil
	}
	return records
}

func groupRecordsByIntent(records []runRecord) map[string][]runRecord {
	grouped := make(map[string][]runRecord)
	for _, record := range records {
		intent := strings.TrimSpace(record.Intent)
		grouped[intent] = append(grouped[intent], record)
	}
	return grouped
}

func popNextRecord(grouped map[string][]runRecord, intent string) (runRecord, bool) {
	records := grouped[intent]
	if len(records) == 0 {
		return runRecord{}, false
	}
	record := records[0]
	grouped[intent] = records[1:]
	return record, true
}

func lastAttempt(attempts []attemptRecord) *attemptRecord {
	if len(attempts) == 0 {
		return nil
	}
	attempt := attempts[len(attempts)-1]
	return &attempt
}

func summarizeOutput(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	truncated := false
	if len(lines) > maxOutputLines {
		lines = lines[:maxOutputLines]
		truncated = true
	}
	summary := strings.Join(lines, "\n")
	if len(summary) > maxOutputChars {
		summary = summary[:maxOutputChars]
		truncated = true
	}
	summary = strings.TrimRight(summary, "\n")
	if truncated {
		summary += "\n... (truncated)"
	}
	return summary
}

func pickOutput(messages []minisweagent.Message, assistantIdx int, attempt *attemptRecord) string {
	// Prefer the observation paired with the assistant message, falling back to attempt output.
	obs := observationAfter(messages, assistantIdx)
	if obs != "" {
		return obs
	}
	if attempt != nil {
		return attempt.Output
	}
	return ""
}

func observationAfter(messages []minisweagent.Message, assistantIdx int) string {
	for i := assistantIdx + 1; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role == "assistant" {
			break
		}
		if msg.Role != "user" {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		lower := strings.ToLower(content)
		if strings.HasPrefix(lower, "observation:") {
			if idx := strings.Index(content, ":"); idx >= 0 {
				return strings.TrimSpace(content[idx+1:])
			}
			return strings.TrimSpace(content[len("Observation:"):])
		}
	}
	return ""
}

func deriveFailureSummary(record runRecord) string {
	last := lastAttempt(record.Attempts)
	if last == nil {
		return "command failed"
	}
	if last.Error != "" {
		return strings.TrimSpace(last.Error)
	}
	if last.ReturnCode != 0 {
		return fmt.Sprintf("command exited with code %d", last.ReturnCode)
	}
	return "command failed"
}

func deriveStepTitle(thought string) string {
	trimmed := strings.TrimSpace(thought)
	if trimmed == "" {
		return ""
	}
	firstLine := trimmed
	if idx := strings.Index(trimmed, "\n"); idx >= 0 {
		firstLine = trimmed[:idx]
	}
	if len(firstLine) > 72 {
		firstLine = firstLine[:72]
		if space := strings.LastIndex(firstLine, " "); space >= 40 {
			firstLine = firstLine[:space]
		}
		firstLine = strings.TrimSpace(firstLine)
		firstLine += "..."
	}
	return firstLine
}
