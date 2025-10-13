package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func Chat(ctx context.Context, modelAlias string, extraParams map[string]any, messages []Message) (string, error) {
	resolved, err := ResolveModel(modelAlias)
	if err != nil {
		return "", err
	}
	return chat(ctx, resolved, extraParams, messages, false, nil)
}

func ChatStream(ctx context.Context, modelAlias string, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	resolved, err := ResolveModel(modelAlias)
	if err != nil {
		return "", err
	}
	return chat(ctx, resolved, extraParams, messages, true, onToken)
}

func ChatWithResolved(ctx context.Context, model ResolvedModel, extraParams map[string]any, messages []Message) (string, error) {
	return chat(ctx, model, extraParams, messages, false, nil)
}

func ChatStreamWithResolved(ctx context.Context, model ResolvedModel, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	return chat(ctx, model, extraParams, messages, true, onToken)
}

var streamingHTTPClient = &http.Client{Timeout: 60 * time.Minute}

const testStubEnv = "MCT_LLM_TEST_STUB" // test-only knob to bypass network LLM calls

func chat(ctx context.Context, model ResolvedModel, extraParams map[string]any, messages []Message, stream bool, onToken func(string)) (string, error) {
	if stub := strings.TrimSpace(os.Getenv(testStubEnv)); stub != "" {
		reply := buildStubResponse(stub, messages)
		if stream && onToken != nil {
			onToken(reply)
		}
		return reply, nil
	}
	if strings.TrimSpace(model.BaseURL) == "" {
		return "", errors.New("resolved model missing base URL")
	}
	if strings.TrimSpace(model.Model) == "" {
		return "", errors.New("resolved model missing upstream model name")
	}
	payload := mergeMaps(model.Params, extraParams)
	payload["model"] = model.Model
	payload["messages"] = messages
	payload["stream"] = stream

	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	req, err := buildRequest(ctx, model, body)
	if err != nil {
		return "", err
	}

	if stream {
		return executeStream(req, onToken)
	}
	return executeOnce(req)
}

func executeStream(req *http.Request, onToken func(string)) (string, error) {
	resp, err := streamingHTTPClient.Do(req)
	if err != nil {
		return "", &UnreachableHostError{URL: req.URL.String(), Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", &HTTPResponseError{URL: req.URL.String(), Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}

	var full strings.Builder
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if strings.HasPrefix(line, "data: ") {
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				if payload == "[DONE]" {
					break
				}
				var obj struct {
					Choices []struct {
						Delta struct {
							Content string `json:"content"`
						} `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal([]byte(payload), &obj); err == nil {
					if len(obj.Choices) > 0 {
						tok := obj.Choices[0].Delta.Content
						if tok != "" {
							if onToken != nil {
								onToken(tok)
							}
							full.WriteString(tok)
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return full.String(), err
		}
	}
	return full.String(), nil
}

func executeOnce(req *http.Request) (string, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", &UnreachableHostError{URL: req.URL.String(), Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", &HTTPResponseError{URL: req.URL.String(), Status: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("no choices returned")
	}
	return parsed.Choices[0].Message.Content, nil
}

func buildRequest(ctx context.Context, model ResolvedModel, body []byte) (*http.Request, error) {
	endpoint := strings.TrimSpace(model.Endpoint)
	base := strings.TrimSpace(model.BaseURL)
	var target string
	switch {
	case endpoint == "":
		target = strings.TrimRight(base, "/") + "/chat/completions"
	case strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://"):
		target = endpoint
	default:
		target = strings.TrimRight(base, "/") + "/" + strings.TrimLeft(endpoint, "/")
	}

	u, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid endpoint %q: %w", target, err)
	}
	q := u.Query()
	for k, v := range model.Query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(model.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+model.APIKey)
	}
	for k, v := range model.Headers {
		req.Header.Set(k, v)
	}
	return req, nil
}

func buildStubResponse(mode string, messages []Message) string {
	content := ""
	if len(messages) > 0 {
		content = messages[len(messages)-1].Content
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		trimmed = "(no prompt provided)"
	}
	if len(trimmed) > 400 {
		trimmed = trimmed[:400] + "\n...[truncated by stub]"
	}
	modeLabel := strings.TrimSpace(mode)
	if modeLabel == "" {
		modeLabel = "default"
	}
	return fmt.Sprintf("Stub LLM (%s) response\n\n%s", modeLabel, trimmed)
}

// Markdown streaming helpers remain unchanged

type MarkdownStreamer struct {
	renderer *glamour.TermRenderer
	buf      strings.Builder
	inCode   bool
}

func NewMarkdownStreamer() (*MarkdownStreamer, error) {
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return nil, err
	}
	return &MarkdownStreamer{renderer: r}, nil
}

func (m *MarkdownStreamer) Feed(tok string) error {
	m.buf.WriteString(tok)
	content := m.buf.String()
	for {
		idx := strings.Index(content, "\n\n")
		if idx == -1 {
			break
		}
		block := content[:idx]
		remaining := content[idx+2:]
		lines := strings.Split(block, "\n")
		for _, ln := range lines {
			l := strings.TrimSpace(ln)
			if strings.HasPrefix(l, "```") {
				m.inCode = !m.inCode
			}
		}
		if m.inCode {
			// hold code blocks until exit fence
		} else {
			out, err := m.renderer.Render(strings.TrimRight(block, "\r\n"))
			if err == nil {
				fmt.Print(strings.TrimRight(out, "\n"))
			}
		}
		content = remaining
		m.buf.Reset()
		m.buf.WriteString(content)
	}
	return nil
}

func (m *MarkdownStreamer) Flush() error {
	s := strings.TrimRight(m.buf.String(), "\r\n")
	if s == "" {
		return nil
	}
	out, err := m.renderer.Render(s)
	if err != nil {
		return err
	}
	fmt.Print(strings.TrimRight(out, "\n"))
	m.buf.Reset()
	return nil
}
