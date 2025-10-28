package discovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

func TestRun_FinalBlockRetryInvalidThenMissing(t *testing.T) {
	t.Helper()

	oldChat := chatInvoker
	defer func() { chatInvoker = oldChat }()

	calls := 0
	chatInvoker = func(ctx context.Context, llmCfg LLMSettings, msgs []chatMessage) (string, error) {
		calls++
		switch calls {
		case 1:
			return "BEGIN_RELEVANT_FILES[file-discovery]\n/abs/path\nEND_RELEVANT_FILES[file-discovery]\n", nil
		case 2:
			last := msgs[len(msgs)-1].Content
			if !strings.Contains(last, "invalid paths") {
				t.Fatalf("expected invalid-path nudge before second attempt; got %q", last)
			}
			return "No block present here", nil
		case 3:
			last := msgs[len(msgs)-1].Content
			if !strings.Contains(last, "block was missing") {
				t.Fatalf("expected missing-block nudge before third attempt; got %q", last)
			}
			return "BEGIN_RELEVANT_FILES[file-discovery]\ngood/path.txt\nEND_RELEVANT_FILES[file-discovery]\n", nil
		default:
			return "", errors.New("unexpected call")
		}
	}

	cfg := cfgpkg.Config{
		MaxRounds:    3,
		NoTrajectory: true,
		ToolCallMode: cfgpkg.ToolCallModeJSON,
	}
	llmCfg := LLMSettings{
		Model: llm.ResolvedModel{
			APIKey:  "key",
			BaseURL: "http://example",
			Model:   "test",
		},
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	exit := Run(context.Background(), cfg, llmCfg)

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	r.Close()

	if exit != 0 {
		t.Fatalf("expected success exit code; got %d", exit)
	}
	if calls != 3 {
		t.Fatalf("expected 3 chat invocations; got %d", calls)
	}
	out := buf.String()
	if !strings.Contains(out, "BEGIN_RELEVANT_FILES[file-discovery]") || !strings.Contains(out, "good/path.txt") {
		t.Fatalf("expected normalized final block in stdout; got %q", out)
	}
}

func TestRun_ForcedFinalizationRetries(t *testing.T) {
	t.Helper()

	oldChat := chatInvoker
	defer func() { chatInvoker = oldChat }()

	calls := 0
	chatInvoker = func(ctx context.Context, llmCfg LLMSettings, msgs []chatMessage) (string, error) {
		calls++
		switch calls {
		case 1:
			return "No tool call yet", nil
		case 2:
			last := msgs[len(msgs)-1].Content
			if !strings.Contains(last, "No other text.") {
				t.Fatalf("expected forced finalization instruction before second attempt; got %q", last)
			}
			return "Still no final block", nil
		case 3:
			last := msgs[len(msgs)-1].Content
			if !strings.Contains(last, "block was missing") {
				t.Fatalf("expected missing-block nudge before third attempt; got %q", last)
			}
			return "BEGIN_RELEVANT_FILES[file-discovery]\n/abs/path\nEND_RELEVANT_FILES[file-discovery]\n", nil
		case 4:
			last := msgs[len(msgs)-1].Content
			if !strings.Contains(last, "invalid paths") {
				t.Fatalf("expected invalid-path nudge before fourth attempt; got %q", last)
			}
			return "BEGIN_RELEVANT_FILES[file-discovery]\nfinal/path.txt\nEND_RELEVANT_FILES[file-discovery]\n", nil
		default:
			return "", errors.New("unexpected call")
		}
	}

	cfg := cfgpkg.Config{
		MaxRounds:    1,
		NoTrajectory: true,
		ToolCallMode: cfgpkg.ToolCallModeJSON,
	}
	llmCfg := LLMSettings{
		Model: llm.ResolvedModel{
			APIKey:  "key",
			BaseURL: "http://example",
			Model:   "test",
		},
	}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	exit := Run(context.Background(), cfg, llmCfg)

	w.Close()
	os.Stdout = oldStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	r.Close()

	if exit != 0 {
		t.Fatalf("expected success exit code; got %d", exit)
	}
	if calls != 4 {
		t.Fatalf("expected 4 chat invocations; got %d", calls)
	}
	out := buf.String()
	if !strings.Contains(out, "BEGIN_RELEVANT_FILES[file-discovery]") || !strings.Contains(out, "final/path.txt") {
		t.Fatalf("expected normalized final block in stdout; got %q", out)
	}
}
