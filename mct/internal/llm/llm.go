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
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string    `json:"model"`
	Stream   bool      `json:"stream"`
	Messages []Message `json:"messages"`
}

// Chat makes a non-streaming chat call and returns the full content
func Chat(ctx context.Context, baseURL, apiKey, model string, messages []Message) (string, error) {
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	reqBody := chatRequest{Model: model, Stream: false, Messages: messages}
	b, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM error %d: %s", resp.StatusCode, string(body))
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

// ChatStream streams tokens using OpenAI-compatible SSE. onToken receives raw deltas.
func ChatStream(ctx context.Context, baseURL, apiKey, model string, messages []Message, onToken func(string)) (string, error) {
	url := strings.TrimRight(baseURL, "/") + "/chat/completions"
	reqBody := chatRequest{Model: model, Stream: true, Messages: messages}
	b, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Try fallback non-streaming
		content, err := Chat(ctx, baseURL, apiKey, model, messages)
		if err != nil {
			body, _ := io.ReadAll(resp.Body)
			return "", fmt.Errorf("LLM stream error %d: %s", resp.StatusCode, string(body))
		}
		if onToken != nil {
			onToken(content)
		}
		return content, nil
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
						if tok != "" && onToken != nil {
							onToken(tok)
						}
						full.WriteString(tok)
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

// MarkdownStreamer renders token deltas with glamour in blocks, preserving code fences.
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

// Feed accepts a token delta and renders when a block boundary is detected.
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
			// accumulate and do not render yet
			// append back and wait for exit
			// keep as is; do nothing extra
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

// Flush renders any remaining buffered content.
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
