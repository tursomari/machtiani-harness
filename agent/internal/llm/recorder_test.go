package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureFile creates a temporary file containing content and returns its
// path along with a cleanup function that removes it.
func writeFixtureFile(t *testing.T, content string) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file %s: %v", path, err)
	}
	return path, func() { _ = os.Remove(path) }
}

func TestRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.jsonl")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture file: %v", err)
	}

	prevWriter := fixtureWriter
	prevIndex := fixtureIndex
	t.Cleanup(func() {
		fixtureWriter = prevWriter
		fixtureIndex = prevIndex
	})

	fixtureWriter = f
	fixtureIndex = 0

	if fixtureWriter == nil {
		t.Fatal("expected fixtureWriter to be non-nil")
	}
	if !IsRecording() {
		t.Fatal("expected IsRecording to return true")
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/api?q=1", nil)

	RecordRoundTrip(
		req,
		[]byte(`{"prompt":"hello"}`),
		200,
		http.Header{"Content-Type": []string{"application/json"}},
		[]byte(`{"response":"world"}`),
	)

	if err := fixtureWriter.Close(); err != nil {
		t.Fatalf("close fixture writer: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture file: %v", err)
	}

	var entry fixtureEntry
	if err := json.Unmarshal([]byte(strings.SplitN(string(data), "\n", 2)[0]), &entry); err != nil {
		t.Fatalf("unmarshal fixture entry: %v", err)
	}

	if entry.Index != 0 {
		t.Fatalf("expected Index 0, got %d", entry.Index)
	}
	if entry.Method != http.MethodGet {
		t.Fatalf("expected Method GET, got %q", entry.Method)
	}
	if !strings.Contains(entry.URL, "example.com/api") {
		t.Fatalf("expected URL to contain example.com/api, got %q", entry.URL)
	}

	var reqBody map[string]string
	if err := json.Unmarshal(entry.RequestBody, &reqBody); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if reqBody["prompt"] != "hello" {
		t.Fatalf("expected request body prompt hello, got %q", reqBody["prompt"])
	}

	if entry.ResponseStatus != 200 {
		t.Fatalf("expected ResponseStatus 200, got %d", entry.ResponseStatus)
	}

	if ct := entry.ResponseHeaders["Content-Type"]; ct != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", ct)
	}

	var respBody map[string]string
	if err := json.Unmarshal(entry.ResponseBody, &respBody); err != nil {
		t.Fatalf("unmarshal response body: %v", err)
	}
	if respBody["response"] != "world" {
		t.Fatalf("expected response body response world, got %q", respBody["response"])
	}
}

func TestRecordRoundTripNoRecording(t *testing.T) {
	prevWriter := fixtureWriter
	prevIndex := fixtureIndex
	t.Cleanup(func() {
		fixtureWriter = prevWriter
		fixtureIndex = prevIndex
	})

	fixtureWriter = nil
	fixtureIndex = 0

	RecordRoundTrip(nil, nil, 0, nil, nil)

	if fixtureIndex != 0 {
		t.Fatalf("expected index to remain 0, got %d", fixtureIndex)
	}
	if IsRecording() {
		t.Fatal("expected IsRecording to return false")
	}
}

func TestInitRecordingWithValidPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.jsonl")
	t.Setenv("LLM_RECORD_FIXTURES", path)

	prevWriter := fixtureWriter
	t.Cleanup(func() {
		fixtureWriter = prevWriter
	})

	fixtureWriter = nil

	// Replicate the init logic from recorder.go.
	f, err := os.Create(os.Getenv("LLM_RECORD_FIXTURES"))
	if err != nil {
		t.Fatalf("init create fixture file: %v", err)
	}
	fixtureWriter = f

	if fixtureWriter == nil {
		t.Fatal("expected fixtureWriter to be non-nil after init")
	}
	if !IsRecording() {
		t.Fatal("expected IsRecording to return true after init")
	}

	if err := f.Close(); err != nil {
		t.Fatalf("close fixture file: %v", err)
	}
}

func TestInitRecordingWithInvalidPath(t *testing.T) {
	t.Setenv("LLM_RECORD_FIXTURES", "/nonexistent/dir/file")

	prevWriter := fixtureWriter
	t.Cleanup(func() {
		fixtureWriter = prevWriter
	})

	fixtureWriter = nil

	// Replicate the init logic from recorder.go.
	f, err := os.Create(os.Getenv("LLM_RECORD_FIXTURES"))
	if err != nil {
		// Expected: directory does not exist.
	} else {
		f.Close()
		fixtureWriter = f
	}

	if fixtureWriter != nil {
		t.Fatal("expected fixtureWriter to remain nil for invalid path")
	}
	if IsRecording() {
		t.Fatal("expected IsRecording to return false for invalid path")
	}
}

func TestCloseRecorder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixtures.jsonl")

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture file: %v", err)
	}

	prevWriter := fixtureWriter
	t.Cleanup(func() {
		fixtureWriter = prevWriter
	})

	fixtureWriter = f

	CloseRecorder()

	if fixtureWriter != nil {
		t.Fatal("expected fixtureWriter to be nil after CloseRecorder")
	}
	if IsRecording() {
		t.Fatal("expected IsRecording to return false after CloseRecorder")
	}
}
