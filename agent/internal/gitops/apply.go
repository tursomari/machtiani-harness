package gitops

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var hunkHeaderRegexp = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$`)

func gitRepoValidator(dir string) error {
	target := strings.TrimSpace(dir)
	if target == "" {
		target = "."
	}

	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = target
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return fmt.Errorf("git command not found; ensure git is installed and in PATH")
		}
		stderrStr := stderr.String()
		if strings.Contains(stderrStr, "not a git repository") {
			return fmt.Errorf("directory %q is not a git repository", target)
		}
		return fmt.Errorf("failed to validate git repository at %q: %w", target, err)
	}
	return nil
}

// ApplyPatch invokes `git apply` against the provided patch file using the current
// working directory.
func ApplyPatch(patchPath string, verbose bool) error {
	return ApplyPatchInDir("", patchPath, verbose)
}

// ApplyPatchInDir invokes `git apply` against the provided patch file from the
// specified repository directory. When verbose is true, it mirrors the original
// CLI logging behaviour from the legacy main.go implementation.
func ApplyPatchInDir(dir, patchPath string, verbose bool) error {
	if err := gitRepoValidator(dir); err != nil {
		return fmt.Errorf("cannot apply patch: %w", err)
	}
	if _, err := os.Stat(patchPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("patch file %q not found", patchPath)
		}
		return fmt.Errorf("cannot read patch file %q: %w", patchPath, err)
	}
	return applyPatch(dir, patchPath, verbose, true, "apply")
}

// ApplyPatchInDirWithCheck runs `git apply --check` for the patch file within the
// provided directory to ensure it can be applied cleanly without modifying the
// workspace.
func ApplyPatchInDirWithCheck(dir, patchPath string) error {
	if err := gitRepoValidator(dir); err != nil {
		return fmt.Errorf("cannot check patch applicability: %w", err)
	}
	if _, err := os.Stat(patchPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("patch file %q not found", patchPath)
		}
		return fmt.Errorf("cannot read patch file %q: %w", patchPath, err)
	}
	return applyPatch(dir, patchPath, false, false, "apply --check", "--check")
}

// ReversePatchInDir applies a pre-generated reverse patch file against the
// provided directory, performing the same git apply invocation used for forward
// patches.
func ReversePatchInDir(dir, patchPath string, verbose bool) error {
	if err := gitRepoValidator(dir); err != nil {
		return fmt.Errorf("cannot apply reverse patch: %w", err)
	}
	if _, err := os.Stat(patchPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("patch file %q not found", patchPath)
		}
		return fmt.Errorf("cannot read patch file %q: %w", patchPath, err)
	}
	return applyPatch(dir, patchPath, verbose, true, "apply (reverse patch)")
}

// ReverseApplyInDir reverses a patch by invoking `git apply -R` against the
// provided patch file within the specified directory. This is more robust for
// atomicity checks than generating a textual reverse patch.
func ReverseApplyInDir(dir, patchPath string, verbose bool) error {
    if err := gitRepoValidator(dir); err != nil {
        return fmt.Errorf("cannot reverse-apply patch: %w", err)
    }
    if _, err := os.Stat(patchPath); err != nil {
        if os.IsNotExist(err) {
            return fmt.Errorf("patch file %q not found", patchPath)
        }
        return fmt.Errorf("cannot read patch file %q: %w", patchPath, err)
    }
    return applyPatch(dir, patchPath, verbose, true, "apply (reverse -R)", "-R")
}

// WorkspaceStatus returns the current `git status --porcelain` entries for the
// provided directory. The results include both tracked and untracked changes.
func WorkspaceStatus(dir string) ([]string, error) {
	if err := gitRepoValidator(dir); err != nil {
		return nil, fmt.Errorf("cannot check workspace status: %w", err)
	}
	dir = strings.TrimSpace(dir)
	cmd := exec.Command("git", "status", "--porcelain", "--ignore-submodules=all")
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	var errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git status failed: %v\n%s", err, trim(errb.String(), 600))
	}
	raw := strings.Split(out.String(), "\n")
	var entries []string
	for _, line := range raw {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entries = append(entries, line)
	}
	return entries, nil
}

// CheckWorkspaceClean returns true if `git status --porcelain` reports no
// modified or untracked files for the provided directory.
func CheckWorkspaceClean(dir string) (bool, error) {
	entries, err := WorkspaceStatus(dir)
	if err != nil {
		return false, fmt.Errorf("cannot check if workspace is clean: %w", err)
	}
	return len(entries) == 0, nil
}

// ReversePatchFromFile calculates the deterministic inverse of the provided
// unified diff patch.
func ReversePatchFromFile(patchPath string) (string, error) {
	data, err := os.ReadFile(patchPath)
	if err != nil {
		return "", fmt.Errorf("read patch file: %w", err)
	}
	normalized := strings.ReplaceAll(string(data), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	hasTrailingNewline := false
	if len(normalized) > 0 && normalized[len(normalized)-1] == '\n' {
		hasTrailingNewline = true
		if len(lines) > 0 {
			lines = lines[:len(lines)-1]
		}
	}

	var reversed []string
	var pendingOldHeader string
	var pendingOldMode string
	var pendingAdditions []string
	var pendingDeletions []string
	var pendingRenameFrom string
	var pendingCopyFrom string

	flushPending := func() {
		if len(pendingAdditions) > 0 {
			reversed = append(reversed, pendingAdditions...)
			pendingAdditions = nil
		}
		if len(pendingDeletions) > 0 {
			reversed = append(reversed, pendingDeletions...)
			pendingDeletions = nil
		}
	}

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushPending()
			reversed = append(reversed, reverseDiffGit(line))
		case strings.HasPrefix(line, "--- "):
			flushPending()
			if pendingOldHeader != "" {
				reversed = append(reversed, pendingOldHeader)
			}
			pendingOldHeader = line
		case strings.HasPrefix(line, "+++ "):
			flushPending()
			if pendingOldHeader != "" {
				oldPath := strings.TrimPrefix(pendingOldHeader, "--- ")
				newPath := strings.TrimPrefix(line, "+++ ")
				reversed = append(reversed, "--- "+newPath)
				reversed = append(reversed, "+++ "+oldPath)
				pendingOldHeader = ""
			} else {
				reversed = append(reversed, line)
			}
		case strings.HasPrefix(line, "index "):
			flushPending()
			reversed = append(reversed, reverseIndexLine(line))
		case strings.HasPrefix(line, "new file mode "):
			flushPending()
			reversed = append(reversed, "deleted file mode "+strings.TrimPrefix(line, "new file mode "))
		case strings.HasPrefix(line, "deleted file mode "):
			flushPending()
			reversed = append(reversed, "new file mode "+strings.TrimPrefix(line, "deleted file mode "))
		case strings.HasPrefix(line, "old mode "):
			flushPending()
			if pendingOldMode != "" {
				reversed = append(reversed, "old mode "+pendingOldMode)
			}
			pendingOldMode = strings.TrimPrefix(line, "old mode ")
		case strings.HasPrefix(line, "new mode "):
			flushPending()
			if pendingOldMode != "" {
				reversed = append(reversed, "old mode "+strings.TrimPrefix(line, "new mode "))
				reversed = append(reversed, "new mode "+pendingOldMode)
				pendingOldMode = ""
			} else {
				reversed = append(reversed, line)
			}
		case strings.HasPrefix(line, "rename from "):
			flushPending()
			pendingRenameFrom = strings.TrimSpace(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			flushPending()
			newName := strings.TrimSpace(strings.TrimPrefix(line, "rename to "))
			if pendingRenameFrom != "" {
				reversed = append(reversed, "rename from "+newName)
				reversed = append(reversed, "rename to "+pendingRenameFrom)
				pendingRenameFrom = ""
			} else {
				reversed = append(reversed, "rename from "+newName)
			}
		case strings.HasPrefix(line, "copy from "):
			flushPending()
			pendingCopyFrom = strings.TrimSpace(strings.TrimPrefix(line, "copy from "))
		case strings.HasPrefix(line, "copy to "):
			flushPending()
			newName := strings.TrimSpace(strings.TrimPrefix(line, "copy to "))
			if pendingCopyFrom != "" {
				reversed = append(reversed, "copy from "+newName)
				reversed = append(reversed, "copy to "+pendingCopyFrom)
				pendingCopyFrom = ""
			} else {
				reversed = append(reversed, "copy from "+newName)
			}
		case strings.HasPrefix(line, "Binary files "):
			flushPending()
			reversed = append(reversed, reverseBinaryDiffLine(line))
		case strings.HasPrefix(line, "@@ "):
			flushPending()
			header, ok := reverseHunkHeader(line)
			if !ok {
				return "", fmt.Errorf("invalid hunk header: %s", line)
			}
			reversed = append(reversed, header)
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			pendingAdditions = append(pendingAdditions, "-"+line[1:])
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			pendingDeletions = append(pendingDeletions, "+"+line[1:])
		default:
			flushPending()
			reversed = append(reversed, line)
		}
	}

	flushPending()
	if pendingOldHeader != "" {
		reversed = append(reversed, pendingOldHeader)
	}
	if pendingOldMode != "" {
		reversed = append(reversed, "old mode "+pendingOldMode)
	}
	if pendingRenameFrom != "" {
		reversed = append(reversed, "rename to "+pendingRenameFrom)
		pendingRenameFrom = ""
	}
	if pendingCopyFrom != "" {
		reversed = append(reversed, "copy to "+pendingCopyFrom)
		pendingCopyFrom = ""
	}

	output := strings.Join(reversed, "\n")
	if hasTrailingNewline {
		output += "\n"
	}
	return output, nil
}

// WriteReversePatchToFile persists the reverse patch contents to the provided output path.
func WriteReversePatchToFile(content, outputPath string) error {
	if err := os.WriteFile(outputPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write reverse patch: %w", err)
	}
	return nil
}

func applyPatch(dir, patchPath string, verbose bool, includeStatus bool, label string, extraArgs ...string) error {
	dir = strings.TrimSpace(dir)
	args := append([]string{"apply"}, extraArgs...)
	args = append(args, patchPath)
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	var errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if verbose {
			fmt.Fprintf(os.Stderr, "[git] %s error for %s: %v\n", label, patchPath, err)
		}
		summary := trim(errb.String(), 600)
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 1:
				return fmt.Errorf("git apply: patch does not apply cleanly\n%s", summary)
			case 128:
				return fmt.Errorf("git apply: internal git error\n%s", summary)
			default:
				return fmt.Errorf("git apply failed (exit code %d): %s", exitErr.ExitCode(), summary)
			}
		}
		return fmt.Errorf("git apply failed: %v\n%s", err, summary)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[git] %s: success\n", label)
		if out.Len() > 0 {
			fmt.Fprintln(os.Stderr, "[git] stdout:", trim(out.String(), 800))
		}
		if includeStatus {
			st := exec.Command("git", "status", "--porcelain")
			if dir != "" {
				st.Dir = dir
			}
			var sb bytes.Buffer
			st.Stdout = &sb
			_ = st.Run()
			if s := strings.TrimSpace(sb.String()); s != "" {
				fmt.Fprintln(os.Stderr, "[git] status:", trim(strings.ReplaceAll(s, "\n", "; "), 800))
			}
		}
	}
	return nil
}

func reverseHunkHeader(line string) (string, bool) {
	matches := hunkHeaderRegexp.FindStringSubmatch(line)
	if matches == nil {
		return "", false
	}
	var b strings.Builder
	b.WriteString("@@ -")
	b.WriteString(matches[3])
	if matches[4] != "" {
		b.WriteString(",")
		b.WriteString(matches[4])
	}
	b.WriteString(" +")
	b.WriteString(matches[1])
	if matches[2] != "" {
		b.WriteString(",")
		b.WriteString(matches[2])
	}
	b.WriteString(" @@")
	b.WriteString(matches[5])
	return b.String(), true
}

func reverseDiffGit(line string) string {
	fields := strings.Fields(line)
	if len(fields) >= 4 {
		aPath := fields[2]
		bPath := fields[3]
		if strings.HasPrefix(aPath, "a/") && strings.HasPrefix(bPath, "b/") {
			fields[2] = "a/" + strings.TrimPrefix(bPath, "b/")
			fields[3] = "b/" + strings.TrimPrefix(aPath, "a/")
			return strings.Join(fields, " ")
		}
	}
	return line
}

func reverseIndexLine(line string) string {
	rest := strings.TrimPrefix(line, "index ")
	parts := strings.Fields(rest)
	if len(parts) == 0 {
		return line
	}
	hashes := strings.Split(parts[0], "..")
	if len(hashes) != 2 {
		return line
	}
	parts[0] = hashes[1] + ".." + hashes[0]
	return "index " + strings.Join(parts, " ")
}

func reverseBinaryDiffLine(line string) string {
	if !strings.HasPrefix(line, "Binary files ") {
		return line
	}
	rest := strings.TrimPrefix(line, "Binary files ")
	parts := strings.SplitN(rest, " and ", 2)
	if len(parts) != 2 {
		return line
	}
	left := strings.TrimSpace(parts[0])
	right := parts[1]
	idx := strings.Index(right, " ")
	if idx == -1 {
		return line
	}
	rightPath := strings.TrimSpace(right[:idx])
	suffix := right[idx:]
	leftSwapped := swapDiffPrefix(rightPath, "b/", "a/")
	rightSwapped := swapDiffPrefix(left, "a/", "b/")
	return "Binary files " + leftSwapped + " and " + rightSwapped + suffix
}

func swapDiffPrefix(path, from, to string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, from) {
		return to + strings.TrimPrefix(path, from)
	}
	return path
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
