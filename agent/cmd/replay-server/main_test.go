package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestReplayServerResponseDelay(t *testing.T) {
	delay := 20 * time.Millisecond
	server := &replayServer{
		entries: []fixtureEntry{{ResponseBody: []byte(`{"ok":true}`)}},
		delay:   delay,
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	started := time.Now()
	server.handler(recorder, request)
	if elapsed := time.Since(started); elapsed < delay {
		t.Fatalf("handler returned after %s, want at least %s", elapsed, delay)
	}
	if got := recorder.Code; got != 200 {
		t.Fatalf("status = %d, want 200", got)
	}
}
