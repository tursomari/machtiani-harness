package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/prompt"
)

const successfulAnswer = "discovery overflow recovery succeeded"

func main() {
	socketPath := strings.TrimSpace(os.Getenv("CONTEXT_OVERFLOW_SMOKE_SOCKET"))
	if socketPath == "" {
		socketPath = "/tmp/context-overflow-smoke.sock"
	}

	var requests atomic.Int32
	handlerErrors := make(chan error, 1)
	reportHandlerError := func(err error) {
		select {
		case handlerErrors <- err:
		default:
		}
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			reportHandlerError(fmt.Errorf("request %d: unexpected %s %s", requestNumber, r.Method, r.URL.Path))
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer smoke-key" {
			reportHandlerError(fmt.Errorf("request %d: missing smoke authorization", requestNumber))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var payload struct {
			Model    string        `json:"model"`
			Messages []llm.Message `json:"messages"`
			Stream   bool          `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			reportHandlerError(fmt.Errorf("request %d: decode payload: %w", requestNumber, err))
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if len(payload.Messages) == 0 {
			reportHandlerError(fmt.Errorf("request %d: empty messages", requestNumber))
			http.Error(w, "unexpected payload", http.StatusBadRequest)
			return
		}

		switch requestNumber {
		case 1:
			if payload.Model != "discovery-model" || payload.Stream {
				reportHandlerError(fmt.Errorf("request %d: want non-stream discovery request, got model=%q stream=%t", requestNumber, payload.Model, payload.Stream))
				http.Error(w, "unexpected discovery payload", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"context_length_exceeded","type":"invalid_request_error"}}`))
		case 2:
			if payload.Model != "discovery-model" || payload.Stream {
				reportHandlerError(fmt.Errorf("request %d: want reduced non-stream discovery request, got model=%q stream=%t", requestNumber, payload.Model, payload.Stream))
				http.Error(w, "unexpected discovery retry payload", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"choices":[{"message":{"content":"BEGIN_RELEVANT_FILES[file-discovery]\nREADME.md\nEND_RELEVANT_FILES[file-discovery]\n"}}]}`)
		case 3:
			if payload.Model != "answer-model" || !payload.Stream {
				reportHandlerError(fmt.Errorf("request %d: want streaming answer request, got model=%q stream=%t", requestNumber, payload.Model, payload.Stream))
				http.Error(w, "unexpected answer payload", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", successfulAnswer)
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		case 4:
			if payload.Model != "answer-model" || payload.Stream {
				reportHandlerError(fmt.Errorf("request %d: want non-stream answer compatibility request, got model=%q stream=%t", requestNumber, payload.Model, payload.Stream))
				http.Error(w, "unexpected answer compatibility payload", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, successfulAnswer)
		default:
			reportHandlerError(fmt.Errorf("unexpected request count %d: model=%q stream=%t", requestNumber, payload.Model, payload.Stream))
			http.Error(w, "too many requests", http.StatusInternalServerError)
		}
	})

	originalTransport := http.DefaultTransport
	if os.Getenv("CONTEXT_OVERFLOW_SMOKE_IN_MEMORY") == "1" {
		http.DefaultTransport = handlerRoundTripper(func(req *http.Request) (*http.Response, error) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			return recorder.Result(), nil
		})
		defer func() { http.DefaultTransport = originalTransport }()
	} else {
		_ = os.Remove(socketPath)
		defer os.Remove(socketPath)
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			fatalf("listen on %s: %v", socketPath, err)
		}
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		}}
		http.DefaultTransport = transport
		defer func() {
			transport.CloseIdleConnections()
			http.DefaultTransport = originalTransport
		}()

		server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
		serverDone := make(chan error, 1)
		go func() {
			serverDone <- server.Serve(listener)
		}()
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
			if serveErr := <-serverDone; serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				fatalf("serve: %v", serveErr)
			}
		}()
	}

	discoveryModel, err := llm.ResolveModel("discovery-overflow")
	if err != nil {
		fatalf("resolve discovery overflow model: %v", err)
	}
	answerModel, err := llm.ResolveModel("answer-stable")
	if err != nil {
		fatalf("resolve answer model: %v", err)
	}
	result, err := prompt.Run(context.Background(), prompt.RunOptions{
		Prompt:                  "Find README.md, then return a deterministic smoke response.",
		FileDiscoveryTrajectory: "/tmp/context-overflow-discovery.jsonl",
		Runtime: prompt.ModelRuntime{
			Resolved:   answerModel,
			Alias:      "answer-stable",
			UsingAlias: true,
		},
		AnswerRuntime: prompt.ModelRuntime{
			Resolved:   answerModel,
			Alias:      "answer-stable",
			UsingAlias: true,
		},
		FileDiscoveryRuntime: prompt.ModelRuntime{
			Resolved:   discoveryModel,
			Alias:      "discovery-overflow",
			UsingAlias: true,
		},
		Prompts: &llm.MCTPromptsConfig{},
	})
	if err != nil {
		fatalf("run prompt: %v", err)
	}
	select {
	case handlerErr := <-handlerErrors:
		fatalf("server assertion: %v", handlerErr)
	default:
	}
	if requests.Load() != 4 {
		fatalf("requests = %d, want 4", requests.Load())
	}
	if result.Assistant != successfulAnswer {
		fatalf("assistant = %q, want %q", result.Assistant, successfulAnswer)
	}
	fmt.Printf("CONTEXT OVERFLOW SMOKE PASSED: discovery_requests=2 answer_requests=2 assistant=%q\n", result.Assistant)
}

type handlerRoundTripper func(*http.Request) (*http.Response, error)

func (fn handlerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "CONTEXT OVERFLOW SMOKE FAILURE: "+format+"\n", args...)
	os.Exit(1)
}
