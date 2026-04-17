package parser

import "testing"

func TestExtractJSONObjectPayload_OK(t *testing.T) {
	body := "Decision: patch\n```json\n{\n  \"edits\": [{\n    \"file\": \"README.md\", \"op\": \"replace\", \"pattern\": \"foo\", \"with\": \"bar\"\n  }],\n  \"metadata\": {\n    \"description\": \"fix readme typo\"\n  }\n}\n```"
	got, err := ExtractJSONObjectPayload(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) == 0 || got[0] != '{' || got[len(got)-1] != '}' {
		t.Fatalf("expected json object, got: %q", string(got))
	}
}

func TestExtractJSONObjectPayload_ToleratesLeadingText(t *testing.T) {
	body := "Monitor verdict follows:\n{\"has_patch_intent\":false,\"reason\":\"single ask\"}"
	got, err := ExtractJSONObjectPayload(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(got) != `{"has_patch_intent":false,"reason":"single ask"}` {
		t.Fatalf("unexpected payload: %q", string(got))
	}
}

func TestExtractJSONObjectPayload_ToleratesTrailingText(t *testing.T) {
	body := "{\"is_mixed\":false,\"reason\":\"already split\",\"rewrite\":\"\"}\nThanks!"
	got, err := ExtractJSONObjectPayload(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(got) != `{"is_mixed":false,"reason":"already split","rewrite":""}` {
		t.Fatalf("unexpected payload: %q", string(got))
	}
}

func TestExtractJSONObjectPayload_BracesInsideString(t *testing.T) {
	body := "```json\n{\"reason\":\"mentions { braces } in text\",\"ok\":true}\n```"
	got, err := ExtractJSONObjectPayload(body)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if string(got) != `{"reason":"mentions { braces } in text","ok":true}` {
		t.Fatalf("unexpected payload: %q", string(got))
	}
}

func TestExtractJSONObjectPayload_NoJSON(t *testing.T) {
	body := "Decision: patch\nNo JSON here"
	if _, err := ExtractJSONObjectPayload(body); err == nil {
		t.Fatalf("expected error when no JSON present")
	}
}

func TestExtractJSONObjectPayload_InvalidJSON(t *testing.T) {
	body := "Decision: patch\n{ not: json }"
	if _, err := ExtractJSONObjectPayload(body); err == nil {
		t.Fatalf("expected error for invalid JSON")
	}
}
