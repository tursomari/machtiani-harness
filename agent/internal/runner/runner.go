package runner

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/mct/llm"
)

type Runner struct {
	Verbose                 bool
	DryRun                  bool
	exePath                 string
	Model                   llm.ResolvedModel
	UsingAlias              bool
	Alias                   string
	FileDiscoveryTrajectory string
}

// Resolve locates the mct binary strictly from PATH (dry-run stores logical name only).
func (r *Runner) Resolve() error {
	if r.DryRun {
		r.exePath = "mct"
		return nil
	}
	p, err := exec.LookPath("mct")
	if err != nil {
		return fmt.Errorf("mct not found in PATH: %w", err)
	}
	r.exePath = p
	return nil
}

// RunPrompt executes `mct prompt --mode=default [--model model] <question>`
// It returns the path mct indicated it saved to if detectable; otherwise empty string.
func (r *Runner) RunPrompt(ctx context.Context, sessionID string, args ...string) (string, error) {
	if r.exePath == "" {
		return "", errors.New("runner unresolved: call Resolve() first")
	}
	env := os.Environ()
	if r.Verbose {
		env = append(env, "MCT_INTERNAL_README_VERBOSE=1")
	}
	if cfgPath, err := llm.ConfigPath(); err == nil && strings.TrimSpace(cfgPath) != "" {
		env = append(env, "MACHTIANI_CONFIG="+cfgPath)
	}
	if !r.UsingAlias {
		if strings.TrimSpace(r.Model.APIKey) != "" {
			env = append(env, "OPENAI_API_KEY="+r.Model.APIKey)
		}
		if strings.TrimSpace(r.Model.BaseURL) != "" {
			env = append(env, "OPENAI_BASE_URL="+r.Model.BaseURL)
		}
		if strings.TrimSpace(r.Model.Model) != "" {
			env = append(env, "OPENAI_MODEL="+r.Model.Model)
		}
	}
	if strings.TrimSpace(r.FileDiscoveryTrajectory) != "" {
		env = append(env, "FILE_DISCOVERY_TRAJECTORY="+r.FileDiscoveryTrajectory)
	}

	// Compose command, inserting --session <id> directly on the CLI if provided.
	finalArgs := make([]string, 0, len(args)+2)
	if len(args) > 0 {
		finalArgs = append(finalArgs, args[0])
		if sessionID != "" {
			finalArgs = append(finalArgs, "--session", sessionID)
		}
		finalArgs = append(finalArgs, args[1:]...)
	} else if sessionID != "" {
		finalArgs = append(finalArgs, "--session", sessionID)
	}
	if len(finalArgs) == 0 {
		finalArgs = append(finalArgs, args...)
	}
	cmd := exec.CommandContext(ctx, r.exePath, finalArgs...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if r.Verbose || r.DryRun {
		fmt.Println("[mct]", r.exePath, strings.Join(finalArgs, " "))
	}
	if r.DryRun {
		return "", nil
	}

	if err := cmd.Run(); err != nil {
		return "", err
	}

	// mct updates .machtiani/chat/machtiani-response.md; return the latest file under that dir if present
	saved := findLatestChatFile(".machtiani/chat")
	return saved, nil
}

func findLatestChatFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var latest string
	var latestMod time.Time
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		// skip our own transcript files (agent-*.md)
		if strings.HasPrefix(name, "agent-") {
			continue
		}
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.ModTime().After(latestMod) {
			latestMod = fi.ModTime()
			latest = p
		}
	}
	return latest
}

func GenerateSessionID() string {
	// Simple timestamp+rand; good enough for correlation
	now := time.Now().UTC().Format("20060102T150405")
	return fmt.Sprintf("agent-%s-%04d", now, rand.Intn(10000))
}
