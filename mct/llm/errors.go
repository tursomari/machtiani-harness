package llm

import "fmt"

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
}

func (e *HTTPResponseError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("llm http error %d: %s (url=%s)", e.Status, e.Body, e.URL)
}
