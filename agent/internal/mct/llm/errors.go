package llm

import (
	"fmt"
	"net/http"
)

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
