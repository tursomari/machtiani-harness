package readmesync

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tursomari/machtiani/agent/internal/mct/internal/git"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/readme"
	promptsvc "github.com/tursomari/machtiani/agent/internal/mct/prompt"
)

// Options describes the inputs required to synchronize the internal README.
type Options struct {
	Commit               string
	Verbose              bool
	MaxInputTokens       int
	Runtime              promptsvc.ModelRuntime
	AnswerRuntime        promptsvc.ModelRuntime
	FileDiscoveryRuntime promptsvc.ModelRuntime
}

// HeadCommit returns the current HEAD commit hash for the repository.
func HeadCommit() (string, error) {
	return git.GetHeadCommitHash()
}

// ResolveCommit resolves an arbitrary commit reference to a full commit hash.
func ResolveCommit(ref string) (string, error) {
	return git.ResolveCommitHash(ref)
}

// Run executes the internal README synchronization workflow.
func Run(ctx context.Context, opts Options) error {
	commit := strings.TrimSpace(opts.Commit)
	if commit == "" {
		return fmt.Errorf("readme sync: commit hash is required")
	}

	mgr, err := readme.NewManager(false, opts.Verbose)
	if err != nil {
		return err
	}
	mgr.SetMaxInputTokens(opts.MaxInputTokens)

	mgr.SetPromptExecutor(func(execCtx context.Context, promptText string) (string, error) {
		prev, hadPrev := os.LookupEnv(readme.SkipReadmeManagerEnv)
		if err := os.Setenv(readme.SkipReadmeManagerEnv, "1"); err != nil {
			return "", fmt.Errorf("set %s: %w", readme.SkipReadmeManagerEnv, err)
		}
		defer func() {
			if hadPrev {
				_ = os.Setenv(readme.SkipReadmeManagerEnv, prev)
			} else {
				_ = os.Unsetenv(readme.SkipReadmeManagerEnv)
			}
		}()

		innerOpts := promptsvc.RunOptions{
			Prompt:               promptText,
			Mode:                 "default",
			IncludeHistory:       false,
			SessionID:            sessionIDForCommit(commit),
			ExplicitName:         "internal-readme",
			Runtime:              opts.Runtime,
			AnswerRuntime:        opts.AnswerRuntime,
			FileDiscoveryRuntime: opts.FileDiscoveryRuntime,
			Verbose:              opts.Verbose,
			MaxInputTokens:       opts.MaxInputTokens,
		}
		res, err := promptsvc.Run(execCtx, innerOpts)
		if err != nil {
			return "", err
		}
		assistant := strings.TrimSpace(res.Assistant)
		if assistant != "" {
			return assistant, nil
		}
		return strings.TrimSpace(res.FullText), nil
	})

	return mgr.Run(ctx, commit)
}

func sessionIDForCommit(commit string) string {
	trimmed := strings.TrimSpace(commit)
	if trimmed == "" {
		return "readme"
	}
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	return fmt.Sprintf("readme-%s", trimmed)
}
