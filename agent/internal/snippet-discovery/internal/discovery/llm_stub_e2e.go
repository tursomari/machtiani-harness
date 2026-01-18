//go:build e2e_stub_llm

package discovery

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
)

var stubCounter int32

func init() {
	chatInvoker = func(ctx context.Context, cfg LLMSettings, msgs []chatMessage) (string, error) {
		_ = ctx
		_ = cfg
		_ = msgs
		seq := strings.TrimSpace(os.Getenv("SNIPPET_DISCOVERY_E2E_RESPONSES"))
		if seq == "" {
			return "{}", nil
		}
		parts := strings.Split(seq, "|")
		idx := int(atomic.AddInt32(&stubCounter, 1)) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(parts) {
			return parts[len(parts)-1], nil
		}
		return parts[idx], nil
	}
}
