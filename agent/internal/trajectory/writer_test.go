package trajectory

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type rawEvent struct {
	Seq int64 `json:"seq"`
}

func TestWriterConcurrentEmit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "agent.jsonl")
	w, err := New(Config{SessionID: "session-test", Path: path, Component: "agent"})
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })

	const goroutines = 8
	const perGoroutine = 25

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				payload := map[string]any{"event_version": 1, "g": id, "j": j}
				if err := w.Emit(context.Background(), Event{Level: "info", Kind: "test.event", SpanID: NewSpanID(), Payload: payload}); err != nil {
					t.Errorf("emit error: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// Close to flush to disk
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024), 1<<20)
	expected := goroutines * perGoroutine
	seenSeq := make(map[int64]struct{}, expected)
	count := 0
	for scanner.Scan() {
		count++
		var evt rawEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			t.Fatalf("unmarshal line %d: %v", count, err)
		}
		if evt.Seq == 0 {
			t.Fatalf("line %d missing seq", count)
		}
		if _, ok := seenSeq[evt.Seq]; ok {
			t.Fatalf("duplicate seq %d", evt.Seq)
		}
		seenSeq[evt.Seq] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != expected {
		t.Fatalf("expected %d events, got %d", expected, count)
	}
}

func TestMakeTextExcerpt(t *testing.T) {
	text := "abcdefghijklmnopqrstuvwxyz"
	excerpt := MakeTextExcerpt(text, 5)
	if excerpt.Len != len(text) {
		t.Fatalf("expected len %d, got %d", len(text), excerpt.Len)
	}
	if excerpt.First != "abcde" {
		t.Fatalf("unexpected first: %q", excerpt.First)
	}
	if excerpt.Last != "vwxyz" {
		t.Fatalf("unexpected last: %q", excerpt.Last)
	}
	if excerpt.SHA == "" {
		t.Fatalf("expected hash")
	}

	zero := MakeTextExcerpt(text, 0)
	if zero.First != "" || zero.Last != "" {
		t.Fatalf("expected empty excerpts when disabled")
	}
	if zero.Len != len(text) {
		t.Fatalf("expected len retained")
	}
}

func TestMakeTextExcerptUnicode(t *testing.T) {
	text := "😀😀😀😀😀"
	excerpt := MakeTextExcerpt(text, 2)
	if excerpt.First != "😀😀" {
		t.Fatalf("unexpected first unicode excerpt: %q", excerpt.First)
	}
	if excerpt.Last != "😀😀" {
		t.Fatalf("unexpected last unicode excerpt: %q", excerpt.Last)
	}
}

func TestMergeExcerpt(t *testing.T) {
	excerpt := MakeTextExcerpt("hello world", 3)
	payload := MergeExcerpt(nil, excerpt)
	if payload["text_len"] != len("hello world") {
		t.Fatalf("expected len filled")
	}
	if payload["text_sha256"] == "" {
		t.Fatalf("expected hash entry")
	}
	if payload["text_excerpt_first"] == "" || payload["text_excerpt_last"] == "" {
		t.Fatalf("expected excerpts present")
	}
}

func TestMergeExcerptWithPrefix(t *testing.T) {
	excerpt := MakeTextExcerpt("hello world", 3)
	payload := MergeExcerptWithPrefix(nil, excerpt, "prompt")
	if payload["prompt_len"] != len("hello world") {
		t.Fatalf("expected prompt_len filled")
	}
	if payload["prompt_sha256"] == "" {
		t.Fatalf("expected prompt hash")
	}
	if payload["prompt_excerpt_first"] == "" || payload["prompt_excerpt_last"] == "" {
		t.Fatalf("expected prompt excerpts present")
	}
}

func TestContextHelpers(t *testing.T) {
	ctx := context.Background()
	got, ok := FromContext(ctx)
	if ok || got != nil {
		t.Fatalf("expected empty writer")
	}
	w := &Writer{}
	ctx = ContextWithWriter(ctx, w)
	found, ok := FromContext(ctx)
	if !ok || found != w {
		t.Fatalf("expected stored writer")
	}
}
