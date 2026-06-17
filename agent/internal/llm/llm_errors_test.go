package llm

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type fakeNetError struct {
	msg       string
	timeout   bool
	temporary bool
}

func (e *fakeNetError) Error() string   { return e.msg }
func (e *fakeNetError) Timeout() bool   { return e.timeout }
func (e *fakeNetError) Temporary() bool { return e.temporary }

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
	_, _, err = performStream(req, nil)
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
	_, _, err = performStream(req, nil)
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
	_, _, err = performNonStream(req)
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
	_, _, err = performNonStream(req)
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

func TestShouldRetry_NetworkErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "bare ECONNRESET",
			err:  &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
			want: true,
		},
		{
			name: "bare ECONNREFUSED",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED},
			want: true,
		},
		{
			name: "url.Error wrapping net.OpError",
			err: &url.Error{
				Op:  "Get",
				URL: "http://example.com",
				Err: &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET},
			},
			want: true,
		},
		{
			name: "broken pipe (EPIPE)",
			err:  &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE},
			want: true,
		},
		{
			name: "net.Error timeout",
			err:  &fakeNetError{msg: "dummy timeout", timeout: true, temporary: true},
			want: true,
		},
		{
			name: "context.Canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "context.DeadlineExceeded",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "HTTPResponseError 400",
			err:  &HTTPResponseError{Status: 400},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRetry(tt.err)
			if got != tt.want {
				t.Errorf("shouldRetry() = %v, want %v", got, tt.want)
			}
		})
	}
}
