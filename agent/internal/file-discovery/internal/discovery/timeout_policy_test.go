package discovery

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
	"github.com/tursomari/machtiani/agent/internal/llm"
)

func runDiscoveryWithDiscardedStdout(t *testing.T, ctx context.Context, cfg cfgpkg.Config) int {
	t.Helper()
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = devNull
	t.Cleanup(func() {
		os.Stdout = original
		_ = devNull.Close()
	})
	return Run(ctx, cfg, LLMSettings{Model: llm.ResolvedModel{APIKey: "key", BaseURL: "http://example", Model: "test"}})
}

func TestRunAppliesPositiveLLMTimeout(t *testing.T) {
	original := chatInvoker
	t.Cleanup(func() { chatInvoker = original })

	chatInvoker = func(ctx context.Context, _ LLMSettings, _ []chatMessage) (string, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("expected discovery call deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > 1100*time.Millisecond {
			t.Fatalf("unexpected remaining deadline: %v", remaining)
		}
		return "BEGIN_RELEVANT_FILES[file-discovery]\nREADME.md\nEND_RELEVANT_FILES[file-discovery]\n", nil
	}

	exit := runDiscoveryWithDiscardedStdout(t, context.Background(), cfgpkg.Config{
		MaxRounds:     1,
		LLMTimeoutSec: 1,
		NoTrajectory:  true,
		ToolCallMode:  cfgpkg.ToolCallModeJSON,
	})
	if exit != 0 {
		t.Fatalf("Run() exit = %d, want 0", exit)
	}
}

func TestRunLeavesUnlimitedLLMContextUnchanged(t *testing.T) {
	original := chatInvoker
	t.Cleanup(func() { chatInvoker = original })
	parent := context.WithValue(context.Background(), struct{}{}, "sentinel")

	chatInvoker = func(ctx context.Context, _ LLMSettings, _ []chatMessage) (string, error) {
		if ctx != parent {
			t.Fatal("unlimited discovery call did not receive the parent context unchanged")
		}
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("unlimited discovery call unexpectedly has a deadline")
		}
		return "BEGIN_RELEVANT_FILES[file-discovery]\nREADME.md\nEND_RELEVANT_FILES[file-discovery]\n", nil
	}

	exit := runDiscoveryWithDiscardedStdout(t, parent, cfgpkg.Config{
		MaxRounds:     1,
		LLMTimeoutSec: 0,
		NoTrajectory:  true,
		ToolCallMode:  cfgpkg.ToolCallModeJSON,
	})
	if exit != 0 {
		t.Fatalf("Run() exit = %d, want 0", exit)
	}
}

func TestRunAppliesTimeoutToForcedFinalization(t *testing.T) {
	original := chatInvoker
	t.Cleanup(func() { chatInvoker = original })
	calls := 0

	chatInvoker = func(ctx context.Context, _ LLMSettings, _ []chatMessage) (string, error) {
		calls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatalf("call %d did not receive a deadline", calls)
		}
		if calls == 1 {
			return "No tool call", nil
		}
		return "BEGIN_RELEVANT_FILES[file-discovery]\nREADME.md\nEND_RELEVANT_FILES[file-discovery]\n", nil
	}

	exit := runDiscoveryWithDiscardedStdout(t, context.Background(), cfgpkg.Config{
		MaxRounds:     1,
		LLMTimeoutSec: 1,
		NoTrajectory:  true,
		ToolCallMode:  cfgpkg.ToolCallModeJSON,
	})
	if exit != 0 {
		t.Fatalf("Run() exit = %d, want 0", exit)
	}
	if calls != 2 {
		t.Fatalf("chat calls = %d, want 2", calls)
	}
}

func TestRunPreservesParentCancellation(t *testing.T) {
	original := chatInvoker
	t.Cleanup(func() { chatInvoker = original })
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var callErr error

	chatInvoker = func(ctx context.Context, _ LLMSettings, _ []chatMessage) (string, error) {
		<-ctx.Done()
		callErr = ctx.Err()
		return "", callErr
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	exit := runDiscoveryWithDiscardedStdout(t, parent, cfgpkg.Config{
		MaxRounds:     1,
		LLMTimeoutSec: 5,
		NoTrajectory:  true,
		ToolCallMode:  cfgpkg.ToolCallModeJSON,
	})
	if exit == 0 {
		t.Fatal("Run() succeeded after parent cancellation")
	}
	if !errors.Is(callErr, context.Canceled) {
		t.Fatalf("chat error = %v, want context.Canceled", callErr)
	}
}
