package readme

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tursomari/machtiani/mct/internal/git"
	"github.com/tursomari/machtiani/mct/internal/utils"
	"github.com/tursomari/machtiani/mct/llm"
)

const (
	repoRelativePath   = ".machtiani/readme"
	readmeFilename     = "internal-readme.md"
	stateDirName       = ".state"
	lastCommitFilename = "last_project_commit"
)

// Manager coordinates readme repository state against the project repository.
type Manager struct {
	ProjectRoot      string
	ReadmeRepoPath   string
	ReadmeFilePath   string
	StateDirPath     string
	LastCommitPath   string
	IsAnswerOnlyMode bool
}

// NewManager returns a manager rooted at the git toplevel containing cwd.
func NewManager(isAnswerOnly bool) (*Manager, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getwd: %w", err)
	}
	projectRoot, err := git.RepoRoot(cwd)
	if err != nil {
		return nil, err
	}
	repoPath := filepath.Join(projectRoot, repoRelativePath)
	stateDir := filepath.Join(repoPath, stateDirName)
	return &Manager{
		ProjectRoot:      projectRoot,
		ReadmeRepoPath:   repoPath,
		ReadmeFilePath:   filepath.Join(repoPath, readmeFilename),
		StateDirPath:     stateDir,
		LastCommitPath:   filepath.Join(stateDir, lastCommitFilename),
		IsAnswerOnlyMode: isAnswerOnly,
	}, nil
}

// Run executes the management workflow using the supplied project commit hash.
func (m *Manager) Run(ctx context.Context, projectCommitHash string) error {
	if strings.TrimSpace(projectCommitHash) == "" {
		return errors.New("project commit hash is required")
	}
	if err := m.ensureRepo(); err != nil {
		return err
	}

	if err := m.ensureStateDir(); err != nil {
		return err
	}

	if err := m.syncExistingReadme(projectCommitHash); err == nil {
		if err := m.writeLastProcessed(projectCommitHash); err != nil {
			utils.LogErrorIfNotAnswerOnly(m.IsAnswerOnlyMode, err, "failed to update last processed commit state")
		}
		return nil
	}

	lastProcessed, _ := m.readLastProcessed()
	needsUpdate, significantFiles, err := m.detectChanges(lastProcessed, projectCommitHash)
	if err != nil {
		utils.LogErrorIfNotAnswerOnly(m.IsAnswerOnlyMode, err, "failed to analyze repository changes")
		needsUpdate = true
	}

	if needsUpdate {
		if err := m.generateAndCommit(ctx, projectCommitHash, lastProcessed, significantFiles); err != nil {
			return err
		}
	} else {
		if err := m.reuseExistingReadme(projectCommitHash); err != nil {
			return err
		}
	}

	if err := m.writeLastProcessed(projectCommitHash); err != nil {
		utils.LogErrorIfNotAnswerOnly(m.IsAnswerOnlyMode, err, "failed to persist last processed commit")
	}
	return nil
}

func (m *Manager) ensureRepo() error {
	if err := os.MkdirAll(m.ReadmeRepoPath, 0o755); err != nil {
		return fmt.Errorf("create readme repo dir: %w", err)
	}
	gitDir := filepath.Join(m.ReadmeRepoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := m.runGit(m.ReadmeRepoPath, "init"); err != nil {
			return err
		}
		if err := m.runGit(m.ReadmeRepoPath, "config", "user.name", "Machtiani README Bot"); err != nil {
			return err
		}
		if err := m.runGit(m.ReadmeRepoPath, "config", "user.email", "readme-bot@machtiani.local"); err != nil {
			return err
		}
		gitignorePath := filepath.Join(m.ReadmeRepoPath, ".gitignore")
		if _, statErr := os.Stat(gitignorePath); os.IsNotExist(statErr) {
			if writeErr := os.WriteFile(gitignorePath, []byte(stateDirName+"/\n"), 0o644); writeErr != nil {
				utils.LogErrorIfNotAnswerOnly(m.IsAnswerOnlyMode, writeErr, "failed to seed .gitignore for readme repo")
			}
		}
	} else if err != nil {
		return fmt.Errorf("stat readme git dir: %w", err)
	}
	return nil
}

func (m *Manager) ensureStateDir() error {
	if err := os.MkdirAll(m.StateDirPath, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	return nil
}

func (m *Manager) syncExistingReadme(projectCommitHash string) error {
	if _, err := GetREADMECommitForProject(projectCommitHash); err != nil {
		return err
	}
	return CheckoutReadonlyReadme(projectCommitHash)
}

func (m *Manager) readLastProcessed() (string, error) {
	data, err := os.ReadFile(m.LastCommitPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (m *Manager) writeLastProcessed(hash string) error {
	return os.WriteFile(m.LastCommitPath, []byte(strings.TrimSpace(hash)), 0o644)
}

func (m *Manager) detectChanges(oldCommit, newCommit string) (bool, []string, error) {
	if strings.TrimSpace(oldCommit) == "" {
		return true, nil, nil
	}
	if oldCommit == newCommit {
		return false, nil, nil
	}
	stdout, err := m.runProjectGit("diff", "--name-only", oldCommit, newCommit)
	if err != nil {
		return true, nil, err
	}
	lines := filterNonEmpty(strings.Split(stdout, "\n"))
	if len(lines) == 0 {
		return false, nil, nil
	}
	sig := make([]string, 0, len(lines))
	for _, file := range lines {
		if isSignificantFile(file) {
			sig = append(sig, file)
		}
	}
	return len(sig) > 0, sig, nil
}

func isSignificantFile(path string) bool {
	lowered := strings.ToLower(path)
	if strings.HasSuffix(lowered, ".md") || strings.HasSuffix(lowered, ".markdown") || strings.HasSuffix(lowered, ".rst") || strings.HasSuffix(lowered, ".txt") {
		return false
	}
	if strings.Contains(lowered, "/docs/") {
		return false
	}
	return true
}

func filterNonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

func (m *Manager) generateAndCommit(ctx context.Context, projectCommitHash, lastProcessed string, significantFiles []string) error {
	content, err := m.buildReadmeContent(ctx, projectCommitHash, lastProcessed, significantFiles)
	if err != nil {
		return err
	}
	if err := os.WriteFile(m.ReadmeFilePath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write readme: %w", err)
	}
	if err := m.runGit(m.ReadmeRepoPath, "add", readmeFilename); err != nil {
		return err
	}
	commitMsg := fmt.Sprintf("README update for project %s", projectCommitHash)
	if err := m.runGit(m.ReadmeRepoPath, "commit", "-m", commitMsg); err != nil {
		return err
	}
	if err := TagREADMEWithProjectCommit(projectCommitHash); err != nil {
		return err
	}
	return nil
}

func (m *Manager) reuseExistingReadme(projectCommitHash string) error {
	if err := TagREADMEWithProjectCommit(projectCommitHash); err != nil {
		return err
	}
	return nil
}

func (m *Manager) buildReadmeContent(ctx context.Context, projectCommitHash, lastProcessed string, significantFiles []string) (string, error) {
	prevContent := ""
	if data, err := os.ReadFile(m.ReadmeFilePath); err == nil {
		prevContent = string(data)
	}
	base := strings.TrimSpace(lastProcessed)
	var diffStat, diffDetail, summary string
	if base != "" {
		diffStat, _ = m.runProjectGit("diff", "--stat", base, projectCommitHash)
		diffDetail, _ = m.runProjectGit("diff", base, projectCommitHash)
		summary, _ = m.runProjectGit("log", "--oneline", fmt.Sprintf("%s..%s", base, projectCommitHash))
	} else {
		diffStat, _ = m.runProjectGit("show", "--stat", projectCommitHash)
		diffDetail, _ = m.runProjectGit("show", projectCommitHash)
		summary, _ = m.runProjectGit("show", "--no-patch", "--pretty=%h %s", projectCommitHash)
	}

	resolved, err := m.resolveModel()
	if err != nil {
		return "", err
	}
	systemPrompt := "You are Machtiani's internal documentation agent. Write a precise, factual internal README for the engineering team. Capture architecture, key services, and any material code changes relevant to this commit. Keep it under 600 words. Use markdown."

	builder := &strings.Builder{}
	builder.WriteString("Project commit: ")
	builder.WriteString(projectCommitHash)
	builder.WriteString("\n\n")
	if lastProcessed != "" {
		builder.WriteString("Previous processed commit: ")
		builder.WriteString(lastProcessed)
		builder.WriteString("\n\n")
	}
	if prevContent != "" {
		builder.WriteString("Previous internal README:\n````markdown\n")
		builder.WriteString(prevContent)
		builder.WriteString("\n````\n\n")
	}
	if len(significantFiles) > 0 {
		builder.WriteString("Files with significant changes since the previous commit:\n")
		for _, f := range significantFiles {
			builder.WriteString("- ")
			builder.WriteString(f)
			builder.WriteString("\n")
		}
		builder.WriteString("\n")
	}
	if strings.TrimSpace(diffStat) != "" {
		builder.WriteString("Diff summary (git diff --stat):\n````\n")
		builder.WriteString(diffStat)
		builder.WriteString("\n````\n\n")
	}
	if strings.TrimSpace(summary) != "" {
		builder.WriteString("Commit log between previous and current:\n````\n")
		builder.WriteString(summary)
		builder.WriteString("\n````\n\n")
	}

	detail := strings.TrimSpace(diffDetail)
	if len(detail) > 20000 {
		detail = detail[:20000] + "\n...\n[diff truncated]"
	}
	if detail != "" {
		builder.WriteString("Relevant diff excerpt:\n````diff\n")
		builder.WriteString(detail)
		builder.WriteString("\n````\n")
	}

	messages := []llm.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: builder.String()},
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	content, err := llm.ChatWithResolved(ctx, resolved, nil, messages)
	if err != nil {
		return "", fmt.Errorf("llm generate readme: %w", err)
	}
	return strings.TrimSpace(content), nil
}

func (m *Manager) resolveModel() (llm.ResolvedModel, error) {
	alias, err := llm.DefaultModelAlias()
	if err != nil {
		return llm.ResolvedModel{}, err
	}
	return llm.ResolveModel(alias)
}

func (m *Manager) runGit(dir string, args ...string) error {
	_, err := m.gitOutput(dir, args...)
	return err
}

func (m *Manager) gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (m *Manager) runProjectGit(args ...string) (string, error) {
	return m.gitOutput(m.ProjectRoot, args...)
}

// CheckoutReadonlyReadme exports the readme file for a tagged project commit into the working tree.
func CheckoutReadonlyReadme(projectCommitHash string) error {
	tag := fmt.Sprintf("oid-%s", projectCommitHash)
	repoPath, err := getReadmeRepoPath()
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "checkout", tag, "--", readmeFilename)
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout %s -- %s failed: %w: %s", tag, readmeFilename, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// TagREADMEWithProjectCommit tags current README commit with project OID.
func TagREADMEWithProjectCommit(projectCommitHash string) error {
	repoPath, err := getReadmeRepoPath()
	if err != nil {
		return err
	}
	tag := fmt.Sprintf("oid-%s", projectCommitHash)
	message := fmt.Sprintf("README for project commit %s", projectCommitHash)
	head, err := gitRevParse(repoPath, "HEAD")
	if err != nil {
		return err
	}
	existing, err := gitRevParse(repoPath, tag)
	if err == nil {
		if existing == head {
			return nil
		}
		if err := runGitCommand(repoPath, "tag", "-d", tag); err != nil {
			return err
		}
	}
	return runGitCommand(repoPath, "tag", "-a", tag, "-m", message)
}

// GetREADMECommitForProject retrieves README version matching project commit.
func GetREADMECommitForProject(projectCommitHash string) (string, error) {
	repoPath, err := getReadmeRepoPath()
	if err != nil {
		return "", err
	}
	tag := fmt.Sprintf("oid-%s", projectCommitHash)
	return gitRevParse(repoPath, tag)
}

func getReadmeRepoPath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	projectRoot, err := git.RepoRoot(cwd)
	if err != nil {
		return "", err
	}
	return filepath.Join(projectRoot, repoRelativePath), nil
}

func gitRevParse(dir, rev string) (string, error) {
	cmd := exec.Command("git", "rev-parse", rev)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git rev-parse %s failed: %w: %s", rev, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runGitCommand(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
