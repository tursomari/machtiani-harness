package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// fixtureEntry mirrors the struct in agent/internal/llm/recorder.go so that the
// replay server can read JSONL fixture files produced by the recording shim.
type fixtureEntry struct {
	Index           int               `json:"index"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	RequestBody     json.RawMessage   `json:"request_body"`
	ResponseStatus  int               `json:"response_status"`
	ResponseHeaders map[string]string `json:"response_headers"`
	ResponseBody    json.RawMessage   `json:"response_body"`
}

type replayServer struct {
	mu      sync.Mutex
	counter int
	entries []fixtureEntry
	delay   time.Duration
}

func (s *replayServer) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	idx := s.counter
	s.counter++
	entries := s.entries
	s.mu.Unlock()

	if idx >= len(entries) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, `{"error": "no more recorded responses; request %d exceeds %d fixtures"}`, idx+1, len(entries))
		return
	}

	entry := entries[idx]
	if s.delay > 0 {
		time.Sleep(s.delay)
	}

	// Default content type if none recorded.
	contentType := "application/json"
	if entry.ResponseHeaders != nil {
		for k, v := range entry.ResponseHeaders {
			if http.CanonicalHeaderKey(k) == "Content-Type" {
				contentType = v
			} else {
				w.Header().Set(k, v)
			}
		}
	}
	w.Header().Set("Content-Type", contentType)

	status := entry.ResponseStatus
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	w.Write([]byte(entry.ResponseBody))
}

func loadFixtures(path string) ([]fixtureEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []fixtureEntry
	dec := json.NewDecoder(f)
	for {
		var entry fixtureEntry
		if err := dec.Decode(&entry); err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("failed to parse fixture line: %w", err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func main() {
	// Use the env var as the default for the port flag if it is set.
	defaultPort := 9876
	if envPort := os.Getenv("REPLAY_SERVER_PORT"); envPort != "" {
		var p int
		if _, err := fmt.Sscanf(envPort, "%d", &p); err == nil && p > 0 {
			defaultPort = p
		}
	}

	port := flag.Int("port", defaultPort, "port to listen on")
	fixturesPath := flag.String("fixtures", "", "path to the JSONL fixture file")
	responseDelay := flag.Duration("response-delay", 0, "delay each recorded response (for timing-sensitive harness checks)")
	flag.Parse()

	if *fixturesPath == "" {
		fmt.Fprintln(os.Stderr, "error: -fixtures flag is required")
		os.Exit(1)
	}

	entries, err := loadFixtures(*fixturesPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: failed to load fixtures: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("replay server: loaded %d fixtures from %s\n", len(entries), *fixturesPath)

	srv := &replayServer{entries: entries, delay: *responseDelay}

	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{Addr: addr, Handler: http.HandlerFunc(srv.handler)}

	// Signal handling for graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	listenErr := make(chan error, 1)
	go func() {
		fmt.Printf("replay server: listening on %s\n", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			listenErr <- err
		}
	}()

	fmt.Println("replay server: press Ctrl+C to stop")

	select {
	case sig := <-stop:
		fmt.Println("replay server: shutting down")
		_ = sig
		os.Exit(0)
	case err := <-listenErr:
		fmt.Fprintf(os.Stderr, "error: server failed: %v\n", err)
		os.Exit(1)
	}
}
