package gitops

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ApplyPatch invokes `git apply` against the provided patch file using the current
// working directory.
func ApplyPatch(patchPath string, verbose bool) error {
	return ApplyPatchInDir("", patchPath, verbose)
}

// ApplyPatchInDir invokes `git apply` against the provided patch file from the
// specified repository directory. When verbose is true, it mirrors the original
// CLI logging behaviour from the legacy main.go implementation.
func ApplyPatchInDir(dir, patchPath string, verbose bool) error {
	cmd := exec.Command("git", "apply", patchPath)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	var errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[git] apply error for %s: %v\n", patchPath, err)
		}
		return fmt.Errorf("git apply failed: %v\n%s", err, trim(errb.String(), 600))
	}
	if verbose {
		fmt.Fprintln(os.Stderr, "[git] apply: success")
		if out.Len() > 0 {
			fmt.Fprintln(os.Stderr, "[git] stdout:", trim(out.String(), 800))
		}
		st := exec.Command("git", "status", "--porcelain")
		if strings.TrimSpace(dir) != "" {
			st.Dir = dir
		}
		var sb bytes.Buffer
		st.Stdout = &sb
		_ = st.Run()
		s := strings.TrimSpace(sb.String())
		if s != "" {
			fmt.Fprintln(os.Stderr, "[git] status:", trim(strings.ReplaceAll(s, "\n", "; "), 800))
		}
	}
	return nil
}

func trim(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
