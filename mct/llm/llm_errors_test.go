package llm

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestExecuteStreamUnreachableHostError(t *testing.T) {
	originalClient := streamingHTTPClient
	errSentinel := errors.New("dial tcp timeout")
	streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errSentinel
	})}
	t.Cleanup(func() {
		streamingHTTPClient = originalClient
	})

	req, err := http.NewRequest(http.MethodPost, "http://example.com/chat", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = performStream(req, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var ue *UnreachableHostError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnreachableHostError, got %T", err)
	}
	if ue.URL != "http://example.com/chat" {
		t.Fatalf("unexpected URL: %s", ue.URL)
	}
	if !errors.Is(ue.Err, errSentinel) {
		t.Fatalf("expected underlying error to be %v, got %v (%T)", errSentinel, ue.Err, ue.Err)
	}
}

func TestExecuteStreamHTTPResponseError(t *testing.T) {
	originalClient := streamingHTTPClient
	streamingHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "502 Bad Gateway",
			StatusCode: http.StatusBadGateway,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Body:       io.NopCloser(strings.NewReader("bad gateway")),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}
	t.Cleanup(func() {
		streamingHTTPClient = originalClient
	})

	req, err := http.NewRequest(http.MethodPost, "http://example.com/chat", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = performStream(req, nil)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var he *HTTPResponseError
	if !errors.As(err, &he) {
		t.Fatalf("expected HTTPResponseError, got %T: %v", err, err)
	}
	if he.Status != http.StatusBadGateway {
		t.Fatalf("unexpected status: %d", he.Status)
	}
	if he.Body != "bad gateway" {
		t.Fatalf("unexpected body: %q", he.Body)
	}
	if he.URL != "http://example.com/chat" {
		t.Fatalf("unexpected URL: %s", he.URL)
	}
}

func TestExecuteOnceUnreachableHostError(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	errSentinel := errors.New("connection refused")
	http.DefaultClient.Transport = roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errSentinel
	})
	t.Cleanup(func() {
		http.DefaultClient.Transport = originalTransport
	})

	req, err := http.NewRequest(http.MethodPost, "http://example.com/chat", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = performNonStream(req)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var ue *UnreachableHostError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnreachableHostError, got %T", err)
	}
	if ue.URL != "http://example.com/chat" {
		t.Fatalf("unexpected URL: %s", ue.URL)
	}
	if !errors.Is(ue.Err, errSentinel) {
		t.Fatalf("expected underlying error to be %v, got %v (%T)", errSentinel, ue.Err, ue.Err)
	}
}

func TestExecuteOnceHTTPResponseError(t *testing.T) {
	originalTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			Status:     "503 Service Unavailable",
			StatusCode: http.StatusServiceUnavailable,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Body:       io.NopCloser(strings.NewReader("service unavailable")),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})
	t.Cleanup(func() {
		http.DefaultClient.Transport = originalTransport
	})

	req, err := http.NewRequest(http.MethodPost, "http://example.com/chat", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	_, err = performNonStream(req)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	var he *HTTPResponseError
	if !errors.As(err, &he) {
		t.Fatalf("expected HTTPResponseError, got %T: %v", err, err)
	}
	if he.Status != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", he.Status)
	}
	if he.Body != "service unavailable" {
		t.Fatalf("unexpected body: %q", he.Body)
	}
	if he.URL != "http://example.com/chat" {
		t.Fatalf("unexpected URL: %s", he.URL)
	}
}
