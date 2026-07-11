package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

type fixtureEntry struct {
	Index            int               `json:"index"`
	Method           string            `json:"method"`
	URL              string            `json:"url"`
	RequestBody      json.RawMessage   `json:"request_body"`
	ResponseStatus   int               `json:"response_status"`
	ResponseHeaders  map[string]string `json:"response_headers"`
	ResponseBody     json.RawMessage   `json:"response_body"`
}

var (
	fixtureMu     sync.Mutex
	fixtureIndex  int
	fixtureWriter io.WriteCloser
)

func IsRecording() bool {
	return fixtureWriter != nil
}

func RecordRoundTrip(req *http.Request, reqBody []byte, respStatus int, respHeaders http.Header, respBody []byte) {
	if fixtureWriter == nil {
		return
	}
	fixtureMu.Lock()
	defer fixtureMu.Unlock()

	headers := make(map[string]string)
	for k, v := range respHeaders {
		if len(v) > 0 {
			headers[k] = v[0]
		}
	}

	entry := fixtureEntry{
		Index:           fixtureIndex,
		Method:          req.Method,
		URL:             req.URL.String(),
		RequestBody:     json.RawMessage(reqBody),
		ResponseStatus:  respStatus,
		ResponseHeaders: headers,
		ResponseBody:    json.RawMessage(respBody),
	}
	fixtureIndex++

	if err := json.NewEncoder(fixtureWriter).Encode(entry); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to encode fixture entry: %v\n", err)
	}
}

func CloseRecorder() {
	if fixtureWriter != nil {
		fixtureWriter.Close()
		fixtureWriter = nil
	}
}

func init() {
	path := os.Getenv("LLM_RECORD_FIXTURES")
	if path == "" {
		return
	}
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create fixture file %s: %v\n", path, err)
		return
	}
	fixtureWriter = f
}
