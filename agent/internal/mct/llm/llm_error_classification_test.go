package llm

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"testing"
)

func TestClassifyErrorNetworkCodes(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		category string
		code     string
	}{
		{
			name:     "dns",
			err:      &net.DNSError{Err: "no such host"},
			category: "network",
			code:     "dns_error",
		},
		{
			name:     "connection refused",
			err:      &url.Error{Err: &net.OpError{Err: syscall.ECONNREFUSED}},
			category: "network",
			code:     "connection_refused",
		},
		{
			name:     "deadline exceeded",
			err:      context.DeadlineExceeded,
			category: "timeout",
			code:     "deadline_exceeded",
		},
		{
			name:     "string unreachable",
			err:      errString("network is unreachable"),
			category: "network",
			code:     "network_unreachable",
		},
		{
			name:     "http 429",
			err:      &HTTPResponseError{Status: http.StatusTooManyRequests},
			category: "http",
			code:     "rate_limited",
		},
	}

	for _, tc := range tests {
		category, code := ClassifyError(tc.err)
		if category != tc.category || code != tc.code {
			t.Fatalf("%s: expected (%s,%s) got (%s,%s)", tc.name, tc.category, tc.code, category, code)
		}
	}
}

type errString string

func (e errString) Error() string { return string(e) }
