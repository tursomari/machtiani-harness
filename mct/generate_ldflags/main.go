package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func mustGit(args ...string) string {
	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running git %s: %v\n", strings.Join(args, " "), err)
		os.Exit(1)
	}
	return strings.TrimSpace(string(output))
}

func gitDirtyState() string {
	cmd := exec.Command("git", "status", "--porcelain", "--untracked-files=no")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	if strings.TrimSpace(string(output)) == "" {
		return "clean"
	}
	return "dirty"
}

func main() {
	shortCommit := mustGit("rev-parse", "--short=12", "HEAD")
	buildDate := time.Now().UTC().Format(time.RFC3339)
	dirty := gitDirtyState()
	version := "dev-" + shortCommit
	if dirty == "dirty" {
		version += "-dirty"
	}

	ldflags := []string{
		"-X 'github.com/tursomari/machtiani/mct/internal/cli.SystemMessageFrequencyHours=24'",
		fmt.Sprintf("-X 'github.com/tursomari/machtiani/mct/internal/cli.SystemMessageRemote=%s'", "https://github.com/tursomari/machtiani"),
		fmt.Sprintf("-X 'github.com/tursomari/machtiani/mct/internal/cli.Version=%s'", version),
		fmt.Sprintf("-X 'github.com/tursomari/machtiani/mct/internal/cli.Commit=%s'", shortCommit),
		fmt.Sprintf("-X 'github.com/tursomari/machtiani/mct/internal/cli.BuiltAt=%s'", buildDate),
		fmt.Sprintf("-X 'github.com/tursomari/machtiani/mct/internal/cli.Dirty=%s'", dirty),
	}

	fmt.Println(strings.Join(ldflags, " "))
}
