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
)

const (
	repoRelativePath     = ".machtiani/readme"
	readmeFilename       = "internal-readme.md"
	stateDirName         = ".state"
	lastCommitFilename   = "last_project_commit"
	mockReadmeEnv        = "MCT_README_TEST_STUB"     // enables deterministic content for integration tests
	SkipReadmeManagerEnv = "MCT_SKIP_INTERNAL_README" // disables recursive manager execution when invoking the CLI
	verboseEnv           = "MCT_INTERNAL_README_VERBOSE"
)

// Manager coordinates readme repository state against the project repository.
type Manager struct {
	ProjectRoot      string
	ReadmeRepoPath   string
	ReadmeFilePath   string
	StateDirPath     string
	LastCommitPath   string
	IsAnswerOnlyMode bool
	Verbose          bool
}

// NewManager returns a manager rooted at the git toplevel containing cwd.
func NewManager(isAnswerOnly bool, verbose bool) (*Manager, error) {
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
	envVerbose := strings.TrimSpace(os.Getenv(verboseEnv)) != ""

	return &Manager{
		ProjectRoot:      projectRoot,
		ReadmeRepoPath:   repoPath,
		ReadmeFilePath:   filepath.Join(repoPath, readmeFilename),
		StateDirPath:     stateDir,
		LastCommitPath:   filepath.Join(stateDir, lastCommitFilename),
		IsAnswerOnlyMode: isAnswerOnly,
		Verbose:          verbose || envVerbose,
	}, nil
}

// Run executes the management workflow using the supplied project commit hash.
func (m *Manager) Run(ctx context.Context, projectCommitHash string) error {
	if strings.TrimSpace(projectCommitHash) == "" {
		return errors.New("project commit hash is required")
	}
	repoInitialized, err := m.ensureRepo()
	if err != nil {
		return err
	}
	if repoInitialized {
		m.verbosef("initialized internal README repo at %s", m.ReadmeRepoPath)
	}

	stateCreated, err := m.ensureStateDir()
	if err != nil {
		return err
	}
	if stateCreated {
		m.verbosef("created state directory %s", m.StateDirPath)
	}

	if err := m.syncExistingReadme(projectCommitHash); err == nil {
		m.verbosef("readme already tagged for project %s; syncing existing content", shortHash(projectCommitHash))
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
		m.verbosef("regenerating README for project %s (base %s, %d significant files)", shortHash(projectCommitHash), shortHash(lastProcessed), len(significantFiles))
		if err := m.generateAndCommit(ctx, projectCommitHash, lastProcessed, significantFiles); err != nil {
			return err
		}
	} else {
		m.verbosef("no significant changes for project %s; reusing existing README", shortHash(projectCommitHash))
		if err := m.reuseExistingReadme(projectCommitHash); err != nil {
			return err
		}
	}

	if err := m.writeLastProcessed(projectCommitHash); err != nil {
		utils.LogErrorIfNotAnswerOnly(m.IsAnswerOnlyMode, err, "failed to persist last processed commit")
	}
	m.verbosef("updated last processed commit marker to %s", shortHash(projectCommitHash))
	return nil
}

func (m *Manager) ensureRepo() (bool, error) {
	created := false
	if err := os.MkdirAll(m.ReadmeRepoPath, 0o755); err != nil {
		return created, fmt.Errorf("create readme repo dir: %w", err)
	}
	gitDir := filepath.Join(m.ReadmeRepoPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		if err := m.runGit(m.ReadmeRepoPath, "init"); err != nil {
			return created, err
		}
		if err := m.runGit(m.ReadmeRepoPath, "config", "user.name", "Machtiani README Bot"); err != nil {
			return created, err
		}
		if err := m.runGit(m.ReadmeRepoPath, "config", "user.email", "readme-bot@machtiani.local"); err != nil {
			return created, err
		}
		gitignorePath := filepath.Join(m.ReadmeRepoPath, ".gitignore")
		if _, statErr := os.Stat(gitignorePath); os.IsNotExist(statErr) {
			if writeErr := os.WriteFile(gitignorePath, []byte(stateDirName+"/\n"), 0o644); writeErr != nil {
				utils.LogErrorIfNotAnswerOnly(m.IsAnswerOnlyMode, writeErr, "failed to seed .gitignore for readme repo")
			}
		}
		created = true
	} else if err != nil {
		return created, fmt.Errorf("stat readme git dir: %w", err)
	}
	return created, nil
}

func (m *Manager) ensureStateDir() (bool, error) {
	created := false
	if _, err := os.Stat(m.StateDirPath); os.IsNotExist(err) {
		created = true
	}
	if err := os.MkdirAll(m.StateDirPath, 0o755); err != nil {
		return created, fmt.Errorf("create state dir: %w", err)
	}
	return created, nil
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
	newHead, headErr := m.gitOutput(m.ReadmeRepoPath, "rev-parse", "HEAD")
	if headErr == nil {
		m.verbosef("committed README update for project %s as %s", shortHash(projectCommitHash), shortHash(strings.TrimSpace(newHead)))
	}
	if err := TagREADMEWithProjectCommit(projectCommitHash); err != nil {
		return err
	}
	m.verbosef("tagged README commit with oid-%s", shortHash(projectCommitHash))
	return nil
}

func (m *Manager) reuseExistingReadme(projectCommitHash string) error {
	if err := TagREADMEWithProjectCommit(projectCommitHash); err != nil {
		return err
	}
	m.verbosef("updated tag oid-%s to current README HEAD", shortHash(projectCommitHash))
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

	if stub := strings.TrimSpace(os.Getenv(mockReadmeEnv)); stub != "" {
		return buildMockReadme(stub, projectCommitHash, base, prevContent, significantFiles, diffStat, summary, diffDetail), nil
	}

	systemPrompt := "You are Machtiani's internal documentation agent. Write a precise, factual internal README for the engineering team. Capture architecture, key services, and any material code changes relevant to this commit. Keep it under 600 words. Use markdown."

	hasExistingReadme := strings.TrimSpace(prevContent) != ""
	var dynamicContext string
	if hasExistingReadme {
		builder := &strings.Builder{}
		builder.WriteString("Project commit: ")
		builder.WriteString(projectCommitHash)
		builder.WriteString("\n\n")
		if lastProcessed != "" {
			builder.WriteString("Previous processed commit: ")
			builder.WriteString(lastProcessed)
			builder.WriteString("\n\n")
		}
		builder.WriteString("Previous internal README:\n````markdown\n")
		builder.WriteString(prevContent)
		builder.WriteString("\n````\n\n")
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
		dynamicContext = builder.String()
	}

	fullPrompt := composeMCTPrompt(systemPrompt, dynamicContext, lastProcessed)

	cmdCtx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()

	args := []string{"run", "--max-steps=5", "--no-patch", "--final-file=" + m.ReadmeFilePath, "--timeout-per-turn=0", fullPrompt}
	cmd := exec.CommandContext(cmdCtx, "mct-agent", args...)
	cmd.Dir = m.ProjectRoot
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=1", SkipReadmeManagerEnv))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("mct-agent run failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if fileContent, err := os.ReadFile(m.ReadmeFilePath); err == nil && strings.TrimSpace(string(fileContent)) != "" {
		return strings.TrimSpace(string(fileContent)), nil
	}
	if content, err := m.readLatestResponse(); err == nil && strings.TrimSpace(content) != "" {
		return strings.TrimSpace(content), nil
	}
	return strings.TrimSpace(extractAssistantContent(stdout.String())), nil
}

func composeMCTPrompt(systemPrompt, dynamicContext, lastProcessed string) string {
	if strings.TrimSpace(lastProcessed) == "" {
		return systemPrompt
	}
	if strings.TrimSpace(dynamicContext) == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n" + dynamicContext
}

func limitLines(input string, maxLines int) string {
	lines := strings.Split(input, "\n")
	if len(lines) > maxLines {
		lines = append(lines[:maxLines], "...[truncated]")
	}
	return strings.Join(lines, "\n")
}

func limitLength(input string, max int) string {
	if len(input) <= max {
		return input
	}
	if max < 0 {
		return input
	}
	if max > len(input) {
		max = len(input)
	}
	return input[:max] + "\n...[diff truncated by mock]\n"
}

func buildMockReadme(mode, projectCommitHash, lastProcessed, prevContent string, significant []string, diffStat, summary, diffDetail string) string {
	b := &strings.Builder{}
	b.WriteString("# Internal README (Mock)\n\n")
	b.WriteString(fmt.Sprintf("- Project commit: %s\n", projectCommitHash))
	if strings.TrimSpace(lastProcessed) != "" {
		b.WriteString(fmt.Sprintf("- Base commit: %s\n", lastProcessed))
	} else {
		b.WriteString("- Base commit: <none>\n")
	}
	b.WriteString(fmt.Sprintf("- Previous README chars: %d\n\n", len(prevContent)))
	b.WriteString("## Significant Files\n")
	if len(significant) == 0 {
		b.WriteString("* none\n")
	} else {
		const maxFiles = 20
		for i, file := range significant {
			if i >= maxFiles {
				b.WriteString(fmt.Sprintf("* ... %d more files\n", len(significant)-i))
				break
			}
			b.WriteString("* " + file + "\n")
		}
	}
	b.WriteString("\n## Diff Summary\n```\n")
	trimmedStat := strings.TrimSpace(diffStat)
	if trimmedStat == "" {
		b.WriteString("<no diff stat available>\n")
	} else {
		b.WriteString(limitLines(trimmedStat, 10) + "\n")
	}
	b.WriteString("```\n")
	if trimmedSummary := strings.TrimSpace(summary); trimmedSummary != "" {
		b.WriteString("\n## Commit Log\n```\n")
		b.WriteString(limitLines(trimmedSummary, 8) + "\n")
		b.WriteString("```\n")
	}
	if detail := strings.TrimSpace(diffDetail); detail != "" {
		detail = limitLines(detail, 20)
		detail = limitLength(detail, 400)
		b.WriteString("\n## Diff Excerpt\n```\n")
		b.WriteString(detail + "\n")
		b.WriteString("```\n")
	}
	b.WriteString("\nMock mode: " + strings.TrimSpace(mode) + "\n")
	return b.String()
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

func extractAssistantContent(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return trimmed
	}
	lines := strings.Split(trimmed, "\n")
	markerIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "# Assistant" {
			markerIdx = i
		}
	}
	if markerIdx == -1 {
		return trimmed
	}
	if markerIdx+1 >= len(lines) {
		return ""
	}
	section := strings.Join(lines[markerIdx+1:], "\n")
	return strings.TrimSpace(section)
}

func shortHash(hash string) string {
	clean := strings.TrimSpace(hash)
	if len(clean) <= 12 {
		return clean
	}
	return clean[:12]
}

func (m *Manager) verbosef(format string, args ...interface{}) {
	if !m.Verbose {
		return
	}
	fmt.Fprintf(os.Stderr, "[readme] "+format+"\n", args...)
}

func (m *Manager) readLatestResponse() (string, error) {
	path := filepath.Join(m.ProjectRoot, ".machtiani", "chat", "machtiani-response.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
