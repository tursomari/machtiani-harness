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
	"syscall"

	"github.com/tursomari/machtiani/agent/internal/git"
	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/mct/artifacts"
	"github.com/tursomari/machtiani/agent/internal/mct/internal/utils"
	"github.com/tursomari/machtiani/agent/internal/prompts"
	"github.com/tursomari/machtiani/agent/internal/templates"
)

const (
	repoRelativePath     = ".machtiani/artifacts/readme"
	readmeFilename       = "internal-readme.md"
	stateDirName         = ".state"
	lastCommitFilename   = "last_project_commit"
	syncLockFilename     = "sync.lock"
	mockReadmeEnv        = "MCT_README_TEST_STUB"     // enables deterministic content for integration tests
	SkipReadmeManagerEnv = "MCT_SKIP_INTERNAL_README" // disables recursive manager execution when invoking the CLI
	verboseEnv           = "MCT_INTERNAL_README_VERBOSE"
)

type PromptExecutor func(ctx context.Context, prompt string) (string, error)

// Manager coordinates readme repository state against the project repository.
type Manager struct {
	ProjectRoot      string
	ReadmeRepoPath   string
	ReadmeFilePath   string
	StateDirPath     string
	LastCommitPath   string
	IsAnswerOnlyMode bool
	Verbose          bool
	PromptExecutor   PromptExecutor
	maxInputTokens   int
	Prompts          *llm.MCTPromptsConfig
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
	repoPath, err := artifacts.ReadmeDirectory()
	if err != nil {
		return nil, err
	}
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

// SetPromptExecutor configures the callback used to generate README content.
func (m *Manager) SetPromptExecutor(exec PromptExecutor) {
	m.PromptExecutor = exec
}

// SetPrompts configures template-driven system prompts for the readme manager.
func (m *Manager) SetPrompts(cfg *llm.MCTPromptsConfig) {
	m.Prompts = cfg
}

// SetMaxInputTokens propagates the maximum prompt size constraint from the agent runtime.
func (m *Manager) SetMaxInputTokens(tokens int) {
	m.maxInputTokens = tokens
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
	lockFile, err := m.acquireSyncLock()
	if err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()

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

func (m *Manager) acquireSyncLock() (*os.File, error) {
	path := filepath.Join(m.StateDirPath, syncLockFilename)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open internal README sync lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock internal README sync state: %w", err)
	}
	return file, nil
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

	systemPrompt, err := m.systemPrompt()
	if err != nil {
		return "", err
	}

	hasExistingReadme := strings.TrimSpace(prevContent) != "" && strings.TrimSpace(lastProcessed) != ""
	var dynamicContext string
	if hasExistingReadme {
		builder := &strings.Builder{}
		builder.WriteString("Update the internal README so it reads as comprehensive documentation of the current system. Use the following cues only to understand what materially changed; do not refer to commits, hashes, diffs, or change logs in the README output.\n\n")
		if strings.TrimSpace(projectCommitHash) != "" {
			builder.WriteString("Current commit (context only): ")
			builder.WriteString(projectCommitHash)
			builder.WriteString("\n")
		}
		if strings.TrimSpace(lastProcessed) != "" {
			builder.WriteString("Last processed commit (context only): ")
			builder.WriteString(lastProcessed)
			builder.WriteString("\n")
		}
		builder.WriteString("\nPrevious internal README:\n````markdown\n")
		builder.WriteString(prevContent)
		builder.WriteString("\n````\n\n")
		if len(significantFiles) > 0 {
			builder.WriteString("Files with significant changes (use as guidance only; do not enumerate these files verbatim in the README):\n")
			for _, f := range significantFiles {
				builder.WriteString("- ")
				builder.WriteString(f)
				builder.WriteString("\n")
			}
			builder.WriteString("\n")
		}
		if strings.TrimSpace(diffStat) != "" {
			builder.WriteString("Diff summary (context only, do not restate in README):\n````\n")
			builder.WriteString(diffStat)
			builder.WriteString("\n````\n\n")
		}
		if strings.TrimSpace(summary) != "" {
			builder.WriteString("Commit log highlights (context only, translate into timeless documentation):\n````\n")
			builder.WriteString(summary)
			builder.WriteString("\n````\n\n")
		}

		detail := strings.TrimSpace(diffDetail)
		if detail != "" {
			builder.WriteString("Relevant diff excerpt (context only; extract concepts rather than quoting diffs):\n````diff\n")
			builder.WriteString(detail)
			builder.WriteString("\n````\n")
		}
		dynamicContext = builder.String()
	}

	fullPrompt := composeMCTPrompt(systemPrompt, dynamicContext, lastProcessed)
	if m.maxInputTokens > 0 {
		estimated := llm.EstimateTokens(fullPrompt)
		if estimated > m.maxInputTokens {
			isFirstCreation := strings.TrimSpace(lastProcessed) == "" && len(significantFiles) == 0
			if isFirstCreation {
				m.verbosef("prompt exceeds max tokens even in first-creation mode (%d > %d); proceeding with minimal context", estimated, m.maxInputTokens)
			} else {
				m.infof("prompt exceeded max input tokens (%d > %d); regenerating internal README with minimal context", estimated, m.maxInputTokens)
				return m.buildReadmeContent(ctx, projectCommitHash, "", nil)
			}
		}
	}

	if m.PromptExecutor == nil {
		return "", errors.New("readme prompt executor is not configured")
	}
	content, err := m.PromptExecutor(ctx, fullPrompt)
	if err != nil {
		return "", fmt.Errorf("generate readme content: %w", err)
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", errors.New("readme prompt executor returned empty content")
	}
	return trimmed, nil
}

func (m *Manager) systemPrompt() (string, error) {
	var tmpl string
	if m.Prompts != nil {
		tmpl = strings.TrimSpace(m.Prompts.ReadmeSystemTemplate)
	}
	if tmpl == "" {
		if embedded, err := templates.GetEmbeddedTemplate("mct.readme_system_template"); err == nil {
			tmpl = embedded
		}
	}
	if strings.TrimSpace(tmpl) == "" {
		return "", fmt.Errorf("readme system prompt template not configured")
	}
	rendered, err := prompts.Render("readme_system_prompt", tmpl, nil, nil)
	if err != nil {
		return "", fmt.Errorf("render readme system prompt: %w", err)
	}
	return strings.TrimSpace(rendered), nil
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

// ReadREADMEForProject reads the README blob associated with a project commit
// directly from the immutable Git object. It does not trust or mutate the
// shared README worktree.
func ReadREADMEForProject(projectCommitHash string) (string, string, error) {
	repoPath, err := getReadmeRepoPath()
	if err != nil {
		return "", "", err
	}
	return readREADMEForProjectAt(repoPath, projectCommitHash)
}

// ReadREADMEForProjectAt reads the tagged README for the project containing
// projectRoot, independent of the process working directory.
func ReadREADMEForProjectAt(projectRoot, projectCommitHash string) (string, string, error) {
	repoPath, err := artifacts.ReadmeDirectoryAt(projectRoot)
	if err != nil {
		return "", "", err
	}
	return readREADMEForProjectAt(repoPath, projectCommitHash)
}

func readREADMEForProjectAt(repoPath, projectCommitHash string) (string, string, error) {
	readmeCommit, err := getREADMECommitForProjectAt(repoPath, projectCommitHash)
	if err != nil {
		return "", "", err
	}
	content, err := gitCommandOutput(repoPath, "show", fmt.Sprintf("%s:%s", readmeCommit, readmeFilename))
	if err != nil {
		return "", "", fmt.Errorf("read internal README at commit %s: %w", readmeCommit, err)
	}
	return content, readmeCommit, nil
}

// CheckoutReadonlyReadme exports the README for a tagged project commit into
// the compatibility artifact path. Injection reads the immutable object via
// ReadREADMEForProject and does not rely on this mutable materialization.
func CheckoutReadonlyReadme(projectCommitHash string) error {
	repoPath, err := getReadmeRepoPath()
	if err != nil {
		return err
	}
	return checkoutReadonlyReadmeAt(repoPath, projectCommitHash)
}

func checkoutReadonlyReadmeAt(repoPath, projectCommitHash string) error {
	content, _, err := readREADMEForProjectAt(repoPath, projectCommitHash)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(repoPath, readmeFilename), []byte(content), 0o644)
}

// TagREADMEWithProjectCommit tags current README commit with project OID.
func TagREADMEWithProjectCommit(projectCommitHash string) error {
	repoPath, err := getReadmeRepoPath()
	if err != nil {
		return err
	}
	return tagREADMEWithProjectCommitAt(repoPath, projectCommitHash)
}

func tagREADMEWithProjectCommitAt(repoPath, projectCommitHash string) error {
	tag := fmt.Sprintf("oid-%s", projectCommitHash)
	message := fmt.Sprintf("README for project commit %s", projectCommitHash)
	head, err := gitRevParse(repoPath, "HEAD")
	if err != nil {
		return err
	}
	existing, err := gitRevParse(repoPath, tag+"^{commit}")
	if err == nil {
		if existing == head {
			return nil
		}
		return fmt.Errorf("internal README tag %s already maps to %s, refusing to move it to %s", tag, existing, head)
	}
	return runGitCommand(repoPath, "tag", "-a", tag, "-m", message)
}

// GetREADMECommitForProject retrieves README version matching project commit.
func GetREADMECommitForProject(projectCommitHash string) (string, error) {
	repoPath, err := getReadmeRepoPath()
	if err != nil {
		return "", err
	}
	return getREADMECommitForProjectAt(repoPath, projectCommitHash)
}

func getREADMECommitForProjectAt(repoPath, projectCommitHash string) (string, error) {
	tag := fmt.Sprintf("oid-%s", projectCommitHash)
	return gitRevParse(repoPath, tag+"^{commit}")
}

func getReadmeRepoPath() (string, error) {
	return artifacts.ReadmeDirectory()
}

func gitRevParse(dir, rev string) (string, error) {
	stdout, err := gitCommandOutput(dir, "rev-parse", rev)
	return strings.TrimSpace(stdout), err
}

func gitCommandOutput(dir string, args ...string) (string, error) {
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

func writeFileAtomic(path string, data []byte, mode os.FileMode) (retErr error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".internal-readme-*")
	if err != nil {
		return fmt.Errorf("create temporary internal README: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set temporary internal README mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary internal README: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary internal README: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary internal README: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace internal README: %w", err)
	}
	return nil
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

func (m *Manager) infof(format string, args ...interface{}) {
	if m.IsAnswerOnlyMode {
		return
	}
	fmt.Fprintf(os.Stderr, "[readme] "+format+"\n", args...)
}
