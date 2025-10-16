package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/trajectory"
)

func applyTrajectoryEnvOverrides(cfg *legacyConfig) {
	if cfg == nil {
		return
	}
	if v := strings.TrimSpace(os.Getenv("MACHTIANI_TRAJECTORY_FILE")); v != "" {
		cfg.trajectoryFile = v
	}
	if b, ok := envBool("MACHTIANI_NO_TRAJECTORY"); ok && b {
		cfg.noTrajectory = true
	}
	if b, ok := envBool("MACHTIANI_TRAJECTORY_VERBOSE_LLM"); ok && b {
		cfg.trajectoryVerboseLLM = true
	}
	if b, ok := envBool("MACHTIANI_TRAJECTORY_STREAM_TOKENS"); ok && b {
		cfg.trajectoryStreamTokens = true
	}
	if n, ok := envInt("MACHTIANI_TRAJECTORY_EXCERPT"); ok {
		cfg.trajectoryExcerpt = n
	}
	if b, ok := envBool("MACHTIANI_TRAJECTORY_OMIT_REPO_ROOT"); ok && b {
		cfg.trajectoryOmitRepoRoot = true
	}
}

func envBool(name string) (bool, bool) {
	val, ok := os.LookupEnv(name)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, true
	}
}

func envInt(name string) (int, bool) {
	val, ok := os.LookupEnv(name)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return 0, false
	}
	return n, true
}

func newTrajectoryWriter(cfg legacyConfig, sessionID string) (*trajectory.Writer, string, error) {
	if cfg.noTrajectory {
		return nil, "", nil
	}
	path := strings.TrimSpace(cfg.trajectoryFile)
	if path == "" {
		p, err := artifacts.SessionTrajectoryFile(sessionID, "agent")
		if err != nil {
			return nil, "", err
		}
		path = p
	}
	w, err := trajectory.New(trajectory.Config{
		SessionID:    sessionID,
		Path:         path,
		Component:    "agent",
		ExcerptLen:   cfg.trajectoryExcerpt,
		StreamTokens: cfg.trajectoryStreamTokens,
		VerboseLLM:   cfg.trajectoryVerboseLLM,
		OmitRepoRoot: cfg.trajectoryOmitRepoRoot,
	})
	if err != nil {
		return nil, "", fmt.Errorf("create trajectory writer at %s: %w", path, err)
	}
	repoRoot := ""
	if !cfg.trajectoryOmitRepoRoot {
		if isLocal, _ := artifacts.IsLocalContext(); isLocal {
			sessionDir, err := artifacts.SessionDirectory(sessionID)
			if err == nil {
				repoRoot = filepath.Dir(filepath.Dir(filepath.Dir(sessionDir)))
			}
		}
	}
	return w, repoRoot, nil
}

func attachTrajectory(ctx context.Context, w *trajectory.Writer, parent string) context.Context {
	if w == nil {
		return ctx
	}
	ctx = trajectory.ContextWithWriter(ctx, w)
	if parent != "" {
		ctx = trajectory.ContextWithParentSpan(ctx, parent)
	}
	return ctx
}

func makeTurnContext(timeoutSec int) (context.Context, context.CancelFunc) {
	if timeoutSec <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
}
