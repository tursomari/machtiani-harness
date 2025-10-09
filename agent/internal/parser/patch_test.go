package parser

import "testing"

func TestExtractPatchJSONPayload_OK(t *testing.T) {
	body := "Decision: patch\n```json\n{\n  \"edits\": [{\n    \"file\": \"README.md\", \"op\": \"replace\", \"pattern\": \"foo\", \"with\": \"bar\"\n  }],\n  \"metadata\": {\n    \"description\": \"fix readme typo\"\n  }\n}\n```"
	got, err := ExtractPatchJSONPayload(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) == 0 || got[0] != '{' || got[len(got)-1] != '}' {
		t.Fatalf("expected json object, got: %q", string(got))
	}
}

func TestExtractPatchJSONPayload_NoJSON(t *testing.T) {
	body := "Decision: patch\nNo JSON here"
	if _, err := ExtractPatchJSONPayload(body); err == nil {
		t.Fatalf("expected error when no JSON present")
	}
}

func TestExtractPatchJSONPayload_InvalidJSON(t *testing.T) {
	body := "Decision: patch\n{ not: json }"
	if _, err := ExtractPatchJSONPayload(body); err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
}
