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
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/glamour"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

var (
	streamingHTTPClient    = &http.Client{Timeout: 60 * time.Minute}
	nonStreamRetryBackoffs = []time.Duration{1 * time.Second, 3 * time.Second, 5 * time.Second}
)

const (
	testStubEnv   = "MCT_LLM_TEST_STUB" // test-only knob to bypass network LLM calls
	retryAfterCap = 15 * time.Second
)

type partialResponseError struct {
	Prefix string
	Err    error
}

func (e *partialResponseError) Error() string {
	if e == nil {
		return ""
	}
	return e.Err.Error()
}

func (e *partialResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func Chat(ctx context.Context, modelAlias string, extraParams map[string]any, messages []Message) (string, error) {
	resolved, err := ResolveModel(modelAlias)
	if err != nil {
		return "", err
	}
	return chatWithResolvedFallback(ctx, resolved, nil, nil, extraParams, messages, false, nil)
}

func ChatStream(ctx context.Context, modelAlias string, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	resolved, err := ResolveModel(modelAlias)
	if err != nil {
		return "", err
	}
	return chatWithResolvedFallback(ctx, resolved, nil, nil, extraParams, messages, true, onToken)
}

func ChatWithResolved(ctx context.Context, model ResolvedModel, extraParams map[string]any, messages []Message) (string, error) {
	return chatWithResolvedFallback(ctx, model, nil, nil, extraParams, messages, false, nil)
}

func ChatStreamWithResolved(ctx context.Context, model ResolvedModel, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	return chatWithResolvedFallback(ctx, model, nil, nil, extraParams, messages, true, onToken)
}

func ChatWithFallback(ctx context.Context, primaryAlias string, fallbackAliases []string, extraParams map[string]any, messages []Message) (string, error) {
	resolved, err := ResolveModel(primaryAlias)
	if err != nil {
		return "", err
	}
	return chatWithResolvedFallback(ctx, resolved, fallbackAliases, nil, extraParams, messages, false, nil)
}

func ChatStreamWithFallback(ctx context.Context, primaryAlias string, fallbackAliases []string, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	resolved, err := ResolveModel(primaryAlias)
	if err != nil {
		return "", err
	}
	return chatWithResolvedFallback(ctx, resolved, fallbackAliases, nil, extraParams, messages, true, onToken)
}

func ChatWithResolvedFallback(ctx context.Context, model ResolvedModel, fallbackAliases []string, fallbackModels []ResolvedModel, extraParams map[string]any, messages []Message) (string, error) {
	return chatWithResolvedFallback(ctx, model, fallbackAliases, fallbackModels, extraParams, messages, false, nil)
}

func ChatStreamWithResolvedFallback(ctx context.Context, model ResolvedModel, fallbackAliases []string, fallbackModels []ResolvedModel, extraParams map[string]any, messages []Message, onToken func(string)) (string, error) {
	return chatWithResolvedFallback(ctx, model, fallbackAliases, fallbackModels, extraParams, messages, true, onToken)
}

func chatWithResolvedFallback(ctx context.Context, primary ResolvedModel, fallbackAliases []string, fallbackModels []ResolvedModel, extraParams map[string]any, messages []Message, stream bool, onToken func(string)) (string, error) {
	if stub := strings.TrimSpace(os.Getenv(testStubEnv)); stub != "" {
		reply := buildStubResponse(stub, messages)
		if stream && onToken != nil {
			onToken(reply)
		}
		return reply, nil
	}
	if err := validateResolvedModel(primary); err != nil {
		return "", err
	}
	normalizedFallbacks := normalizeFallbackAliases(primary, fallbackAliases)
	targets := buildFallbackTargets(primary, normalizedFallbacks, fallbackModels)

	basePayload := mergeMaps(primary.Params, extraParams)
	basePayload["model"] = primary.Model
	basePayload["messages"] = messages

	nonStreamBody, err := encodePayload(basePayload, false)
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	var result string
	var attemptErr error
	emittedPrefix := ""

	if stream {
		streamBody, err := encodePayload(basePayload, true)
		if err != nil {
			return "", fmt.Errorf("encode request: %w", err)
		}
		result, attemptErr = tryStreamThenFallback(ctx, primary, streamBody, nonStreamBody, onToken)
		if attemptErr == nil {
			return result, nil
		}
		var partial *partialResponseError
		if errors.As(attemptErr, &partial) {
			emittedPrefix = partial.Prefix
			attemptErr = partial.Err
		}
	} else {
		result, attemptErr = executeOnceWithRetries(ctx, primary, nonStreamBody)
		if attemptErr == nil {
			return result, nil
		}
	}

	if len(targets) == 0 {
		if emittedPrefix != "" {
			return "", &partialResponseError{Prefix: emittedPrefix, Err: attemptErr}
		}
		return "", attemptErr
	}

	lastErr := attemptErr
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			if emittedPrefix != "" {
				return "", &partialResponseError{Prefix: emittedPrefix, Err: err}
			}
			return "", err
		}
		var fallbackModel ResolvedModel
		if target.resolved != nil {
			fallbackModel = CloneResolvedModel(*target.resolved)
		} else {
			resolved, err := ResolveModel(target.alias)
			if err != nil {
				lastErr = fmt.Errorf("resolve fallback model %q: %w", target.alias, err)
				logFallbackResolutionError(target.alias, err)
				continue
			}
			fallbackModel = resolved
		}
		if sameResolvedModel(primary, fallbackModel) {
			continue
		}
		if err := validateResolvedModel(fallbackModel); err != nil {
			lastErr = err
			continue
		}
		payload := mergeMaps(fallbackModel.Params, extraParams)
		payload["model"] = fallbackModel.Model
		payload["messages"] = messages

		body, err := encodePayload(payload, false)
		if err != nil {
			lastErr = fmt.Errorf("encode request: %w", err)
			continue
		}

		logModelFailover(primary, fallbackModel)
		result, err = executeOnceWithRetries(ctx, fallbackModel, body)
		if err != nil {
			lastErr = err
			continue
		}
		if stream {
			emitWithPrefix(onToken, emittedPrefix, result, fallbackModel)
		}
		return result, nil
	}

	if emittedPrefix != "" {
		return "", &partialResponseError{Prefix: emittedPrefix, Err: lastErr}
	}
	return "", lastErr
}

func validateResolvedModel(model ResolvedModel) error {
	if strings.TrimSpace(model.BaseURL) == "" {
		return errors.New("resolved model missing base URL")
	}
	if strings.TrimSpace(model.Model) == "" {
		return errors.New("resolved model missing upstream model name")
	}
	return nil
}

func normalizeFallbackAliases(primary ResolvedModel, aliases []string) []string {
	if len(aliases) == 0 {
		return nil
	}
	primaryAlias := strings.TrimSpace(primary.Alias)
	seen := make(map[string]struct{}, len(aliases))
	out := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		trimmed := strings.TrimSpace(alias)
		if trimmed == "" {
			continue
		}
		if trimmed == primaryAlias {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

type fallbackTarget struct {
	alias    string
	resolved *ResolvedModel
}

func buildFallbackTargets(primary ResolvedModel, aliases []string, models []ResolvedModel) []fallbackTarget {
	targets := make([]fallbackTarget, 0, len(aliases)+len(models))
	seen := make(map[string]struct{}, len(aliases)+len(models)+1)
	seen[fallbackResolvedKey(primary)] = struct{}{}
	for _, alias := range aliases {
		key := "alias:" + strings.ToLower(alias)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, fallbackTarget{alias: alias})
	}
	for _, mdl := range models {
		clone := CloneResolvedModel(mdl)
		if sameResolvedModel(primary, clone) {
			continue
		}
		key := fallbackResolvedKey(clone)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, fallbackTarget{resolved: &clone})
	}
	return targets
}

func fallbackResolvedKey(m ResolvedModel) string {
	base := strings.ToLower(strings.TrimSpace(m.BaseURL))
	endpoint := strings.ToLower(strings.TrimSpace(m.Endpoint))
	model := strings.ToLower(strings.TrimSpace(m.Model))
	alias := strings.ToLower(strings.TrimSpace(m.Alias))
	return base + "|" + endpoint + "|" + model + "|" + alias
}

func sameResolvedModel(a, b ResolvedModel) bool {
	return strings.EqualFold(strings.TrimSpace(a.BaseURL), strings.TrimSpace(b.BaseURL)) &&
		strings.EqualFold(strings.TrimSpace(a.Endpoint), strings.TrimSpace(b.Endpoint)) &&
		strings.EqualFold(strings.TrimSpace(a.Model), strings.TrimSpace(b.Model))
}

func encodePayload(base map[string]any, stream bool) ([]byte, error) {
	payload := mergeMaps(base, map[string]any{"stream": stream})
	return json.Marshal(payload)
}

func tryStreamThenFallback(ctx context.Context, model ResolvedModel, streamBody, nonStreamBody []byte, onToken func(string)) (string, error) {
	var streamedPrefix strings.Builder
	emitted := false
	wrapped := func(tok string) {
		streamedPrefix.WriteString(tok)
		if onToken != nil {
			emitted = true
			onToken(tok)
		}
	}

	result, err := executeStream(ctx, model, streamBody, wrapped)
	if err == nil {
		return result, nil
	}
	logStreamingFallback(model, err)
	fallbackResult, fallbackErr := executeOnceWithRetries(ctx, model, nonStreamBody)
	if fallbackErr != nil {
		if emitted {
			return "", &partialResponseError{Prefix: streamedPrefix.String(), Err: fallbackErr}
		}
		return "", fallbackErr
	}
	prefix := ""
	if emitted {
		prefix = streamedPrefix.String()
	}
	emitWithPrefix(onToken, prefix, fallbackResult, model)
	return fallbackResult, nil
}

func executeStream(ctx context.Context, model ResolvedModel, body []byte, onToken func(string)) (string, error) {
	req, err := buildRequest(ctx, model, body)
	if err != nil {
		return "", err
	}
	return performStream(req, onToken)
}

func executeOnceWithRetries(ctx context.Context, model ResolvedModel, body []byte) (string, error) {
	maxAttempts := len(nonStreamRetryBackoffs) + 1
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, err := buildRequest(ctx, model, body)
		if err != nil {
			return "", err
		}
		result, err := performNonStream(req)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !shouldRetry(err) || attempt == maxAttempts {
			return "", err
		}
		wait := retryDelay(err, nonStreamRetryBackoffs[attempt-1])
		logRetryAttempt(model, attempt+1, maxAttempts, wait, err)
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", errors.New("non-stream retries exhausted")
}

func performStream(req *http.Request, onToken func(string)) (string, error) {
	resp, err := streamingHTTPClient.Do(req)
	if err != nil {
		return "", &UnreachableHostError{URL: req.URL.String(), Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", &HTTPResponseError{URL: req.URL.String(), Status: resp.StatusCode, Body: strings.TrimSpace(string(b)), Header: resp.Header.Clone()}
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

func performNonStream(req *http.Request) (string, error) {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", &UnreachableHostError{URL: req.URL.String(), Err: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", &HTTPResponseError{URL: req.URL.String(), Status: resp.StatusCode, Body: strings.TrimSpace(string(body)), Header: resp.Header.Clone()}
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

func emitWithPrefix(onToken func(string), prefix, full string, model ResolvedModel) {
	if onToken == nil {
		return
	}
	if prefix == "" {
		onToken(full)
		return
	}
	if strings.HasPrefix(full, prefix) {
		suffix := full[len(prefix):]
		if suffix != "" {
			onToken(suffix)
		}
		return
	}
	logSuffixMismatch(model)
	onToken(full)
}

func shouldRetry(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ue *UnreachableHostError
	if errors.As(err, &ue) {
		return true
	}
	var httpErr *HTTPResponseError
	if errors.As(err, &httpErr) {
		status := httpErr.Status
		switch status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
			return false
		}
		return isRetryableStatus(status)
	}
	if isAmbiguousProtocolError(err) {
		return true
	}
	return false
}

func isRetryableStatus(status int) bool {
	if status == http.StatusTooManyRequests || status == http.StatusRequestTimeout || status == http.StatusTooEarly {
		return true
	}
	if status >= 500 && status <= 599 {
		return true
	}
	if status >= 520 && status <= 526 { // common CDN/proxy errors
		return true
	}
	return false
}

func isAmbiguousProtocolError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return true
	}
	var unmarshalErr *json.UnmarshalTypeError
	if errors.As(err, &unmarshalErr) {
		return true
	}
	return false
}

func retryDelay(err error, defaultDelay time.Duration) time.Duration {
	var httpErr *HTTPResponseError
	if errors.As(err, &httpErr) {
		if httpErr.Status == http.StatusTooManyRequests || httpErr.Status == http.StatusServiceUnavailable {
			if h := httpErr.Header; h != nil {
				if wait, ok := parseRetryAfter(h); ok {
					if wait > retryAfterCap {
						wait = retryAfterCap
					}
					return wait
				}
			}
		}
	}
	return defaultDelay
}

func parseRetryAfter(header http.Header) (time.Duration, bool) {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(value); err == nil {
		delta := time.Until(t)
		if delta < 0 {
			delta = 0
		}
		return delta, true
	}
	return 0, false
}

func logStreamingFallback(model ResolvedModel, err error) {
	if err == nil {
		return
	}
	var httpErr *HTTPResponseError
	if errors.As(err, &httpErr) {
		url := httpErr.URL
		if url != "" {
			fmt.Fprintf(os.Stderr, "llm: streaming failure for %s (status=%d url=%s); falling back to non-streaming\n", modelDisplayName(model), httpErr.Status, url)
			return
		}
		fmt.Fprintf(os.Stderr, "llm: streaming failure for %s (status=%d); falling back to non-streaming\n", modelDisplayName(model), httpErr.Status)
		return
	}
	fmt.Fprintf(os.Stderr, "llm: streaming failure for %s: %v; falling back to non-streaming\n", modelDisplayName(model), err)
}

func logRetryAttempt(model ResolvedModel, nextAttempt, maxAttempts int, wait time.Duration, err error) {
	fmt.Fprintf(os.Stderr, "llm: retrying %s attempt %d/%d in %s due to %v\n", modelDisplayName(model), nextAttempt, maxAttempts, wait, err)
}

func logModelFailover(primary, fallback ResolvedModel) {
	fmt.Fprintf(os.Stderr, "llm: model failover %s -> %s\n", modelDisplayName(primary), modelDisplayName(fallback))
}

func logSuffixMismatch(model ResolvedModel) {
	fmt.Fprintf(os.Stderr, "llm: non-stream fallback response diverged for %s; emitting full response\n", modelDisplayName(model))
}

func logFallbackResolutionError(alias string, err error) {
	name := strings.TrimSpace(alias)
	if name == "" {
		name = "(empty alias)"
	}
	fmt.Fprintf(os.Stderr, "llm: could not resolve fallback model %s: %v\n", name, err)
}

func modelDisplayName(model ResolvedModel) string {
	if alias := strings.TrimSpace(model.Alias); alias != "" {
		return alias
	}
	if name := strings.TrimSpace(model.Model); name != "" {
		return name
	}
	if endpoint := strings.TrimSpace(model.Endpoint); endpoint != "" {
		return endpoint
	}
	return strings.TrimSpace(model.BaseURL)
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
