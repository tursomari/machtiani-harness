package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

const modelHostProtocolVersion = 1

type modelHostRequest struct {
	Version int               `json:"v"`
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Params  modelHostGenerate `json:"params"`
}

type modelHostGenerate struct {
	Caller          string             `json:"caller"`
	SessionID       string             `json:"sessionId"`
	Messages        []modelHostMessage `json:"messages"`
	Model           string             `json:"model"`
	ReasoningEffort string             `json:"reasoningEffort,omitempty"`
	MaxTokens       int                `json:"maxTokens,omitempty"`
}

type modelHostMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type modelHostEnvelope struct {
	Version int             `json:"v"`
	ID      any             `json:"id"`
	Event   *modelHostEvent `json:"event,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *modelHostFault `json:"error,omitempty"`
}

type modelHostEvent struct {
	Type             string `json:"type"`
	Text             string `json:"text,omitempty"`
	InputTokens      int    `json:"inputTokens,omitempty"`
	OutputTokens     int    `json:"outputTokens,omitempty"`
	TotalTokens      int    `json:"totalTokens,omitempty"`
	CacheReadTokens  int    `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int    `json:"cacheWriteTokens,omitempty"`
}

type modelHostFault struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	RetryAfterMS int    `json:"retryAfterMs,omitempty"`
}

type ModelHostCallError struct {
	Code         string
	Message      string
	RetryAfterMS int
}

func (e *ModelHostCallError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func modelHostSessionID(ctx context.Context) string {
	if value := strings.TrimSpace(os.Getenv("MACHTIANI_SESSION_ID")); value != "" {
		return value
	}
	return "machtiani-call"
}

func chatModelHost(ctx context.Context, model ResolvedModel, extraParams map[string]any, messages []Message, stream bool, onToken func(string)) (string, error) {
	command := strings.TrimSpace(model.Command)
	if command == "" {
		command = "machtiani-model-host"
	}
	profile := strings.TrimSpace(model.Profile)
	if profile == "" {
		return "", errors.New("resolved model-host transport missing profile")
	}
	requestMessages := make([]modelHostMessage, 0, len(messages))
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "system", "user", "assistant":
		default:
			return "", fmt.Errorf("model-host transport does not support message role %q", message.Role)
		}
		requestMessages = append(requestMessages, modelHostMessage{Role: role, Content: message.Content})
	}
	params := modelHostGenerate{
		Caller:    "machtiani",
		SessionID: modelHostSessionID(ctx),
		Messages:  requestMessages,
		Model:     model.Model,
	}
	if effort := modelHostReasoningEffort(model, extraParams); effort != "" {
		params.ReasoningEffort = effort
	}
	if maxTokens := integerParam(extraParams, "max_tokens"); maxTokens > 0 {
		params.MaxTokens = maxTokens
	}
	request := modelHostRequest{Version: modelHostProtocolVersion, ID: "generation", Method: "generation/start", Params: params}
	payload, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("encode model-host request: %w", err)
	}

	cmd := exec.CommandContext(ctx, command, "--profile", profile)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("open model-host input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("open model-host output: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{writer: &stderr, remaining: 64 << 10}
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start model host %q: %w", command, err)
	}
	writeErr := make(chan error, 1)
	go func() {
		_, err := stdin.Write(append(payload, '\n'))
		if closeErr := stdin.Close(); err == nil {
			err = closeErr
		}
		writeErr <- err
	}()

	var answer strings.Builder
	var usage *responseUsage
	completed := false
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var envelope modelHostEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return "", errors.New("model host returned an invalid protocol message")
		}
		if envelope.Version != modelHostProtocolVersion || fmt.Sprint(envelope.ID) != "generation" {
			continue
		}
		if envelope.Error != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return "", &ModelHostCallError{Code: envelope.Error.Code, Message: envelope.Error.Message, RetryAfterMS: envelope.Error.RetryAfterMS}
		}
		if envelope.Event != nil {
			switch envelope.Event.Type {
			case "text-delta":
				answer.WriteString(envelope.Event.Text)
				if stream && onToken != nil {
					onToken(envelope.Event.Text)
				}
			case "usage":
				usage = &responseUsage{
					PromptTokens:     envelope.Event.InputTokens,
					CompletionTokens: envelope.Event.OutputTokens,
					TotalTokens:      envelope.Event.TotalTokens,
					PromptTokensDetails: &promptTokensDetails{
						CachedTokens:     envelope.Event.CacheReadTokens,
						CacheWriteTokens: envelope.Event.CacheWriteTokens,
					},
				}
			}
		}
		if len(envelope.Result) > 0 {
			completed = true
		}
	}
	if scanErr := scanner.Err(); scanErr != nil && ctx.Err() == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", fmt.Errorf("read model-host stream: %w", scanErr)
	}
	if err := <-writeErr; err != nil && ctx.Err() == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return "", fmt.Errorf("write model-host request: %w", err)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if waitErr != nil {
		return "", fmt.Errorf("model host exited before completing the request: %w", waitErr)
	}
	if !completed {
		return "", errors.New("model host ended without a completion result")
	}
	emitCacheUsage(ctx, model, usage)
	return answer.String(), nil
}

func modelHostReasoningEffort(model ResolvedModel, extra map[string]any) string {
	for _, key := range []string{"reasoning_effort", "effort"} {
		if value, ok := extra[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		if value, ok := model.Params[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if reasoning, ok := model.Params["reasoning"].(map[string]any); ok {
		if value, ok := reasoning["effort"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func integerParam(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

type limitedWriter struct {
	writer    io.Writer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	original := len(p)
	if w.remaining <= 0 {
		return original, nil
	}
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	_, err := w.writer.Write(p)
	w.remaining -= len(p)
	return original, err
}
