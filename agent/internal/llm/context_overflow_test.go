package llm

import "testing"

func TestIsContextOverflowStructuredFields(t *testing.T) {
	for _, body := range []string{
		`{"error":{"metadata":{"error_type":"context_length_exceeded"}}}`,
		`{"error":{"code":"context_window_exceeded"}}`,
		`{"error":{"type":"maximum_context_length_exceeded"}}`,
	} {
		if !IsContextOverflow(&HTTPResponseError{Status: 400, Body: body}) {
			t.Fatalf("did not classify %s", body)
		}
	}
}

func TestIsContextOverflowRejectsStatusAndMessage(t *testing.T) {
	for _, err := range []error{
		&HTTPResponseError{Status: 400, Body: `{"error":{"message":"context_length_exceeded"}}`},
		&HTTPResponseError{Status: 413, Body: `{"error":{"code":"request_too_large"}}`},
		&HTTPResponseError{Status: 400, Body: `not json`},
	} {
		if IsContextOverflow(err) {
			t.Fatalf("false positive: %v", err)
		}
	}
}

func TestIsContextOverflowModelHostCode(t *testing.T) {
	if !IsContextOverflow(&ModelHostCallError{Code: "CONTEXT_LENGTH_EXCEEDED"}) {
		t.Fatal("model-host context overflow was not recognized")
	}
	if IsContextOverflow(&ModelHostCallError{Code: "INTERNAL", Message: "CONTEXT_LENGTH_EXCEEDED"}) {
		t.Fatal("error text must not classify an unrelated model-host failure")
	}
}
