package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

var contextOverflowCodes = map[string]struct{}{
	"context_length_exceeded":         {},
	"context_window_exceeded":         {},
	"maximum_context_length_exceeded": {},
}

// IsContextOverflow reports only machine-readable provider or model-host errors. It
// deliberately ignores HTTP status and human-readable message text.
func IsContextOverflow(err error) bool {
	var hostErr *ModelHostCallError
	if errors.As(err, &hostErr) && hostErr != nil {
		return hostErr.Code == "CONTEXT_LENGTH_EXCEEDED"
	}
	var httpErr *HTTPResponseError
	if !errors.As(err, &httpErr) || httpErr == nil || httpErr.Body == "" {
		return false
	}
	var payload struct {
		Error struct {
			Code     any `json:"code"`
			Type     any `json:"type"`
			Metadata struct {
				ErrorType any `json:"error_type"`
			} `json:"metadata"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(httpErr.Body), &payload) != nil {
		return false
	}
	for _, raw := range []any{payload.Error.Metadata.ErrorType, payload.Error.Code, payload.Error.Type} {
		if value, ok := raw.(string); ok {
			if _, matched := contextOverflowCodes[value]; matched {
				return true
			}
		}
	}
	return false
}

// UnreachableHostError wraps transport/network errors when the LLM endpoint cannot be reached.
type UnreachableHostError struct {
	URL string
	Err error
}

func (e *UnreachableHostError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("llm unreachable host %s: %v", e.URL, e.Err)
}

func (e *UnreachableHostError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ProviderTimeoutError wraps a context.DeadlineExceeded-style timeout that
// occurs inside the LLM provider client (not our own per-turn context deadline).
type ProviderTimeoutError struct {
	URL string
	Err error
}

func (e *ProviderTimeoutError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("llm provider timeout %s: %v", e.URL, e.Err)
}

func (e *ProviderTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// HTTPResponseError wraps non-successful HTTP responses from the LLM endpoint.
type HTTPResponseError struct {
	URL    string
	Status int
	Body   string
	Header http.Header
}

func (e *HTTPResponseError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("llm http error %d: %s (url=%s)", e.Status, e.Body, e.URL)
}

// CloneHeaders returns a defensive copy of the HTTP headers associated with the
// error so callers can safely inspect values like Retry-After without risking
// data races.
func (e *HTTPResponseError) CloneHeaders() http.Header {
	if e == nil || len(e.Header) == 0 {
		return http.Header{}
	}
	dup := make(http.Header, len(e.Header))
	for k, vv := range e.Header {
		copyVals := make([]string, len(vv))
		copy(copyVals, vv)
		dup[k] = copyVals
	}
	return dup
}
