package readme

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSanitizeGitEnvRemovesGitAndEmailCaseInsensitively(t *testing.T) {
	t.Setenv("git_dir", "/tmp/hostile-git-dir")
	t.Setenv("Git_Work_Tree", "/tmp/hostile-work-tree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/hostile-index")
	t.Setenv("git_config_global", "/tmp/hostile-config")
	t.Setenv("email", "lower@example.test")
	t.Setenv("EMAIL", "upper@example.test")

	extra := map[string]string{
		"GIT_CONFIG_GLOBAL":      "/tmp/controlled-gitconfig",
		"GIT_CONFIG_NOSYSTEM":    "1",
		"GIT_ATTR_NOSYSTEM":      "1",
		"GIT_TERMINAL_PROMPT":    "0",
		"GIT_PAGER":              "cat",
		"GIT_EDITOR":             "true",
		"GIT_NO_REPLACE_OBJECTS": "1",
		"GIT_OPTIONAL_LOCKS":     "0",
	}
	env := sanitizeGitEnv(extra)
	values := readmeTestEnvValues(env)
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "EMAIL"} {
		if _, ok := values[key]; ok {
			t.Fatalf("sanitized environment retained %s: %q", key, values[key])
		}
	}
	for key, want := range extra {
		if got := values[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if _, ok := values["PATH"]; !ok {
		t.Fatal("sanitized environment removed PATH")
	}
	if _, ok := values["HOME"]; !ok {
		if _, ok := values["USERPROFILE"]; !ok {
			t.Fatal("sanitized environment removed HOME and USERPROFILE")
		}
	}
	if len(values) != len(env) {
		t.Fatalf("sanitized environment contains duplicate case-insensitive keys: %v", env)
	}
	for key := range values {
		if !strings.HasPrefix(key, "GIT_") {
			continue
		}
		if _, ok := extra[key]; !ok {
			t.Fatalf("sanitized environment retained unexpected %s=%q", key, values[key])
		}
	}
}

func TestGitRunnerCommandCarriesControlledConfig(t *testing.T) {
	t.Setenv("git_dir", "/tmp/hostile-git-dir")
	t.Setenv("Git_Work_Tree", "/tmp/hostile-work-tree")
	t.Setenv("GIT_INDEX_FILE", "/tmp/hostile-index")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgSign")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("email", "lower@example.test")
	t.Setenv("EMAIL", "upper@example.test")

	readmeRepo := filepath.Join(t.TempDir(), "readme")
	runner := newReadmeGitRunner(readmeRepo)

	cmd := runner.command(readmeRepo, "commit", "-m", "message")
	values := readmeTestEnvValues(cmd.Env)
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_CONFIG_COUNT", "EMAIL"} {
		if _, ok := values[key]; ok {
			t.Fatalf("controlled environment retained %s: %q", key, values[key])
		}
	}
	if got := values["GIT_CONFIG_GLOBAL"]; got != filepath.Join(readmeRepo, stateDirName, controlledGitConfigFilename) {
		t.Fatalf("GIT_CONFIG_GLOBAL = %q", got)
	}
	if got := values["GIT_CONFIG_NOSYSTEM"]; got != "1" {
		t.Fatalf("GIT_CONFIG_NOSYSTEM = %q, want 1", got)
	}
	if got := values["GIT_TERMINAL_PROMPT"]; got != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, want 0", got)
	}
	if _, ok := values["PATH"]; !ok {
		t.Fatal("controlled environment removed PATH")
	}
	if _, ok := values["HOME"]; !ok {
		if _, ok := values["USERPROFILE"]; !ok {
			t.Fatal("controlled environment removed HOME and USERPROFILE")
		}
	}
	if len(values) != len(cmd.Env) {
		t.Fatalf("controlled environment contains duplicate case-insensitive keys: %v", cmd.Env)
	}
	allowedGitEnv := map[string]struct{}{
		"GIT_CONFIG_GLOBAL":      {},
		"GIT_CONFIG_NOSYSTEM":    {},
		"GIT_ATTR_NOSYSTEM":      {},
		"GIT_TERMINAL_PROMPT":    {},
		"GIT_PAGER":              {},
		"GIT_EDITOR":             {},
		"GIT_NO_REPLACE_OBJECTS": {},
		"GIT_OPTIONAL_LOCKS":     {},
	}
	for key := range values {
		if !strings.HasPrefix(key, "GIT_") {
			continue
		}
		if _, ok := allowedGitEnv[key]; !ok {
			t.Fatalf("controlled environment retained unexpected %s=%q", key, values[key])
		}
	}
	if cmd.Dir != readmeRepo {
		t.Fatalf("cmd.Dir = %q, want %q", cmd.Dir, readmeRepo)
	}

	assertSafeDirs(t, cmd.Args, readmeRepo)
	wantConfig := map[string]string{
		"user.name":           readmeBotName,
		"user.email":          readmeBotEmail,
		"author.name":         readmeBotName,
		"author.email":        readmeBotEmail,
		"committer.name":      readmeBotName,
		"committer.email":     readmeBotEmail,
		"commit.gpgSign":      "false",
		"tag.gpgSign":         "false",
		"core.hooksPath":      filepath.Join(readmeRepo, stateDirName, emptyHooksDirName),
		"core.attributesFile": filepath.Join(readmeRepo, stateDirName, emptyAttributesFilename),
		"core.excludesFile":   filepath.Join(readmeRepo, stateDirName, emptyExcludesFilename),
		"core.autocrlf":       "false",
		"core.fsmonitor":      "false",
		"core.eol":            "lf",
		"core.safecrlf":       "false",
		"color.ui":            "false",
		"log.showSignature":   "false",
		"log.mailmap":         "false",
		"init.defaultBranch":  "main",
	}
	for key, want := range wantConfig {
		if got := readmeTestConfigArg(cmd.Args, key); got != want {
			t.Fatalf("-c %s = %q, want %q", key, got, want)
		}
	}
	if got := strings.Join(cmd.Args[len(cmd.Args)-3:], " "); got != "commit -m message" {
		t.Fatalf("trailing args = %q, want commit -m message", got)
	}

	readCmd := runner.command(readmeRepo, "rev-parse", "HEAD")
	for key, want := range wantConfig {
		if got := readmeTestConfigArg(readCmd.Args, key); got != want {
			t.Fatalf("read -c %s = %q, want %q", key, got, want)
		}
	}
	if got := strings.Join(readCmd.Args[len(readCmd.Args)-2:], " "); got != "rev-parse HEAD" {
		t.Fatalf("read trailing args = %q", got)
	}
}

func TestGitRunnerIgnoresHostileEnvironment(t *testing.T) {
	root := t.TempDir()
	global := writeReadmeSigningGlobal(t, root)

	hostileRepo := t.TempDir()
	runReadmeGitControlled(t, hostileRepo, "init", "-q")
	if err := os.WriteFile(filepath.Join(hostileRepo, "hostile.txt"), []byte("hostile\n"), 0o644); err != nil {
		t.Fatalf("write hostile file: %v", err)
	}
	runReadmeGitControlled(t, hostileRepo, "add", "hostile.txt")
	runReadmeGitControlled(t, hostileRepo, "-c", "user.name=Hostile User", "-c", "user.email=hostile@example.test", "commit", "-q", "-m", "hostile")
	hostileHead := strings.TrimSpace(runReadmeGitControlled(t, hostileRepo, "rev-parse", "HEAD"))
	hostileRefs := runReadmeGitControlled(t, hostileRepo, "for-each-ref", "--format=%(refname) %(objectname)")
	hostileStatus := runReadmeGitControlled(t, hostileRepo, "status", "--porcelain")
	hostileIndex, err := os.ReadFile(filepath.Join(hostileRepo, ".git", "index"))
	if err != nil {
		t.Fatalf("read hostile index: %v", err)
	}

	addHostileGlobalConfig(t, root, global, hostileRepo)

	isolateReadmeGitConfig(t, root, global)
	original, err := os.ReadFile(global)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	t.Setenv(mockReadmeEnv, "basic")

	t.Setenv("GIT_DIR", filepath.Join(hostileRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", hostileRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(hostileRepo, ".git", "index"))
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgSign")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	t.Setenv("GIT_CONFIG_KEY_1", "tag.gpgSign")
	t.Setenv("GIT_CONFIG_VALUE_1", "true")
	t.Setenv("GIT_AUTHOR_NAME", "Personal Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "personal-author@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "Personal Committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "personal-committer@example.test")
	t.Setenv("EMAIL", "personal@example.test")

	manager := newReadmeSigningTestManager(root)
	projectCommit := strings.Repeat("d", 40)
	if err := manager.Run(context.Background(), projectCommit); err != nil {
		t.Fatalf("run readme sync with hostile environment: %v", err)
	}

	assertReadmeHistory(t, manager.ReadmeRepoPath, projectCommit)
	assertReadmeBotConfigControlled(t, manager.ReadmeRepoPath)
	disk, err := os.ReadFile(manager.ReadmeFilePath)
	if err != nil {
		t.Fatalf("read written README: %v", err)
	}
	content := runReadmeGitControlled(t, manager.ReadmeRepoPath, "show", "HEAD:"+readmeFilename)
	if content != string(disk) {
		t.Fatalf("committed README differs from written file:\ncommitted: %q\nwritten: %q", content, disk)
	}
	if !strings.Contains(content, "Mock mode: basic") {
		t.Fatalf("committed README missing mock content: %q", content)
	}

	stateDir := filepath.Join(manager.ReadmeRepoPath, stateDirName)
	if data, err := os.ReadFile(filepath.Join(stateDir, controlledGitConfigFilename)); err != nil {
		t.Fatalf("read controlled gitconfig: %v", err)
	} else if len(data) != 0 {
		t.Fatalf("controlled gitconfig is not empty: %q", data)
	}
	for _, name := range []string{emptyAttributesFilename, emptyExcludesFilename} {
		if data, err := os.ReadFile(filepath.Join(stateDir, name)); err != nil {
			t.Fatalf("read controlled %s: %v", name, err)
		} else if len(data) != 0 {
			t.Fatalf("controlled %s is not empty: %q", name, data)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(stateDir, emptyHooksDirName)); err != nil {
		t.Fatalf("read controlled hooks dir: %v", err)
	} else if len(entries) != 0 {
		t.Fatalf("controlled hooks dir is not empty: %v", entries)
	}

	if got := strings.TrimSpace(runReadmeGitControlled(t, hostileRepo, "rev-parse", "HEAD")); got != hostileHead {
		t.Fatalf("hostile repo HEAD changed from %s to %s", hostileHead, got)
	}
	if got := runReadmeGitControlled(t, hostileRepo, "for-each-ref", "--format=%(refname) %(objectname)"); got != hostileRefs {
		t.Fatalf("hostile repo refs changed:\nbefore:\n%s\nafter:\n%s", hostileRefs, got)
	}
	if got := runReadmeGitControlled(t, hostileRepo, "status", "--porcelain"); got != hostileStatus {
		t.Fatalf("hostile repo status changed:\nbefore:\n%s\nafter:\n%s", hostileStatus, got)
	}
	hostileIndexAfter, err := os.ReadFile(filepath.Join(hostileRepo, ".git", "index"))
	if err != nil {
		t.Fatalf("reread hostile index: %v", err)
	}
	if !bytes.Equal(hostileIndex, hostileIndexAfter) {
		t.Fatal("hostile repo index changed")
	}

	after, err := os.ReadFile(global)
	if err != nil {
		t.Fatalf("reread global config: %v", err)
	}
	if !bytes.Equal(original, after) {
		t.Fatalf("global Git configuration changed:\nbefore:\n%s\nafter:\n%s", original, after)
	}
}

func TestRunRecoversFromLegacyReadmeState(t *testing.T) {
	t.Run("reuse existing README", func(t *testing.T) {
		testRunRecoversFromLegacyReadmeState(t, false)
	})
	t.Run("regenerate README", func(t *testing.T) {
		testRunRecoversFromLegacyReadmeState(t, true)
	})
}

func testRunRecoversFromLegacyReadmeState(t *testing.T, regenerate bool) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("create home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	unsetReadmeGitEnv(t)

	projectRoot := filepath.Join(root, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("create project root: %v", err)
	}
	runReadmeTestGit(t, projectRoot, "init")
	runReadmeTestGit(t, projectRoot, "config", "user.name", "Project User")
	runReadmeTestGit(t, projectRoot, "config", "user.email", "project@example.test")
	if err := os.WriteFile(filepath.Join(projectRoot, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	runReadmeTestGit(t, projectRoot, "add", "main.go")
	runReadmeTestGit(t, projectRoot, "commit", "-m", "initial")
	staleCommit := strings.TrimSpace(runReadmeTestGit(t, projectRoot, "rev-parse", "HEAD"))

	readmeRepo := filepath.Join(root, "readme")
	if err := os.MkdirAll(readmeRepo, 0o755); err != nil {
		t.Fatalf("create readme repo: %v", err)
	}
	runReadmeGitControlled(t, readmeRepo, "init", "-q")
	runReadmeGitControlled(t, readmeRepo, "config", "--local", "user.name", readmeBotName)
	runReadmeGitControlled(t, readmeRepo, "config", "--local", "user.email", readmeBotEmail)
	if err := os.WriteFile(filepath.Join(readmeRepo, ".gitignore"), []byte(stateDirName+"/\n"), 0o644); err != nil {
		t.Fatalf("write legacy gitignore: %v", err)
	}
	runReadmeGitControlled(t, readmeRepo, "add", ".gitignore")
	if err := os.WriteFile(filepath.Join(readmeRepo, readmeFilename), []byte("legacy readme\n"), 0o644); err != nil {
		t.Fatalf("write legacy readme: %v", err)
	}
	runReadmeGitControlled(t, readmeRepo, "add", readmeFilename)
	runReadmeGitControlled(t, readmeRepo, "commit", "-m", "legacy readme")
	legacyHead := strings.TrimSpace(runReadmeGitControlled(t, readmeRepo, "rev-parse", "HEAD"))

	manager := &Manager{
		ProjectRoot:    projectRoot,
		ReadmeRepoPath: readmeRepo,
		ReadmeFilePath: filepath.Join(readmeRepo, readmeFilename),
		StateDirPath:   filepath.Join(readmeRepo, stateDirName),
		LastCommitPath: filepath.Join(readmeRepo, stateDirName, lastCommitFilename),
	}
	if err := os.MkdirAll(manager.StateDirPath, 0o755); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	if err := os.WriteFile(manager.LastCommitPath, []byte(staleCommit), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readmeRepo, ".git", "TAG_EDITMSG"), []byte("leftover\n"), 0o644); err != nil {
		t.Fatalf("write leftover TAG_EDITMSG: %v", err)
	}
	if err := os.WriteFile(manager.ReadmeFilePath, []byte("staged local edit\n"), 0o644); err != nil {
		t.Fatalf("write staged edit: %v", err)
	}
	runReadmeGitControlled(t, readmeRepo, "add", readmeFilename)

	if regenerate {
		if err := os.WriteFile(filepath.Join(projectRoot, "main.go"), []byte("package main\n// changed\n"), 0o644); err != nil {
			t.Fatalf("change main.go: %v", err)
		}
	} else {
		if err := os.MkdirAll(filepath.Join(projectRoot, "docs"), 0o755); err != nil {
			t.Fatalf("create docs: %v", err)
		}
		if err := os.WriteFile(filepath.Join(projectRoot, "docs", "notes.md"), []byte("notes\n"), 0o644); err != nil {
			t.Fatalf("write docs: %v", err)
		}
		runReadmeTestGit(t, projectRoot, "add", "docs/notes.md")
	}
	runReadmeTestGit(t, projectRoot, "add", "main.go")
	runReadmeTestGit(t, projectRoot, "commit", "-m", "project change")
	currentCommit := strings.TrimSpace(runReadmeTestGit(t, projectRoot, "rev-parse", "HEAD"))

	global := writeReadmeSigningGlobal(t, root)
	addHostileGlobalConfig(t, root, global, "")
	isolateReadmeGitConfig(t, root, global)
	originalGlobal, err := os.ReadFile(global)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	t.Setenv(mockReadmeEnv, "basic")

	if err := manager.Run(context.Background(), currentCommit); err != nil {
		t.Fatalf("run readme sync from legacy state: %v", err)
	}

	assertReadmeHistory(t, readmeRepo, currentCommit)
	assertReadmeBotConfigControlled(t, readmeRepo)
	marker, err := os.ReadFile(manager.LastCommitPath)
	if err != nil {
		t.Fatalf("read last processed marker: %v", err)
	}
	if got := strings.TrimSpace(string(marker)); got != currentCommit {
		t.Fatalf("last_project_commit = %q, want %q", got, currentCommit)
	}
	tagTarget := strings.TrimSpace(runReadmeGitControlled(t, readmeRepo, "rev-parse", "oid-"+currentCommit+"^{commit}"))
	if regenerate {
		head := strings.TrimSpace(runReadmeGitControlled(t, readmeRepo, "rev-parse", "HEAD"))
		if head == legacyHead {
			t.Fatal("regenerate path did not create a new README commit")
		}
		if tagTarget != head {
			t.Fatalf("tag target = %s, want %s", tagTarget, head)
		}
		disk, err := os.ReadFile(manager.ReadmeFilePath)
		if err != nil {
			t.Fatalf("read regenerated README: %v", err)
		}
		content := runReadmeGitControlled(t, readmeRepo, "show", "HEAD:"+readmeFilename)
		if content != string(disk) {
			t.Fatalf("committed README differs from written file:\ncommitted: %q\nwritten: %q", content, disk)
		}
		if !strings.Contains(content, "Mock mode: basic") {
			t.Fatalf("regenerated README missing mock content: %q", content)
		}
		if status := runReadmeGitControlled(t, readmeRepo, "status", "--porcelain"); status != "" {
			t.Fatalf("regenerate path left worktree changes: %q", status)
		}
	} else {
		head := strings.TrimSpace(runReadmeGitControlled(t, readmeRepo, "rev-parse", "HEAD"))
		if head != legacyHead {
			t.Fatalf("reuse path moved README HEAD from %s to %s", legacyHead, head)
		}
		if tagTarget != legacyHead {
			t.Fatalf("tag target = %s, want %s", tagTarget, legacyHead)
		}
		content := runReadmeGitControlled(t, readmeRepo, "show", "HEAD:"+readmeFilename)
		if content != "legacy readme\n" {
			t.Fatalf("reuse path changed committed content: %q", content)
		}
		status := runReadmeGitControlled(t, readmeRepo, "status", "--porcelain")
		if !strings.Contains(status, "M  "+readmeFilename) {
			t.Fatalf("reuse path did not preserve staged change: %q", status)
		}
		disk, err := os.ReadFile(manager.ReadmeFilePath)
		if err != nil {
			t.Fatalf("read staged README: %v", err)
		}
		if string(disk) != "staged local edit\n" {
			t.Fatalf("reuse path changed staged worktree file: %q", disk)
		}
	}

	stateConfig, err := os.ReadFile(filepath.Join(manager.StateDirPath, controlledGitConfigFilename))
	if err != nil {
		t.Fatalf("read controlled gitconfig: %v", err)
	}
	if len(stateConfig) != 0 {
		t.Fatalf("controlled gitconfig is not empty: %q", stateConfig)
	}
	afterGlobal, err := os.ReadFile(global)
	if err != nil {
		t.Fatalf("reread global config: %v", err)
	}
	if !bytes.Equal(originalGlobal, afterGlobal) {
		t.Fatalf("global Git configuration changed:\nbefore:\n%s\nafter:\n%s", originalGlobal, afterGlobal)
	}
}

func addHostileGlobalConfig(t *testing.T, root, global, worktree string) {
	t.Helper()
	excludes := filepath.Join(root, "global-excludes")
	if err := os.WriteFile(excludes, []byte(readmeFilename+"\n"), 0o644); err != nil {
		t.Fatalf("write hostile excludes: %v", err)
	}
	attributes := filepath.Join(root, "global-attributes")
	if err := os.WriteFile(attributes, []byte(readmeFilename+" filter=hostile\n"), 0o644); err != nil {
		t.Fatalf("write hostile attributes: %v", err)
	}
	hooks := filepath.Join(root, "global-hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatalf("create hostile hooks dir: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatalf("write hostile hook: %v", err)
		}
	}
	missingClean := filepath.Join(root, "missing-clean-filter")
	for key, value := range map[string]string{
		"core.excludesFile":       excludes,
		"core.attributesFile":     attributes,
		"core.autocrlf":           "true",
		"core.hooksPath":          hooks,
		"filter.hostile.clean":    missingClean,
		"filter.hostile.required": "true",
	} {
		runReadmeTestGit(t, root, "config", "--file", global, key, value)
	}
	if worktree != "" {
		runReadmeTestGit(t, root, "config", "--file", global, "core.worktree", worktree)
	}
}

func assertReadmeHistory(t *testing.T, repo, projectCommit string) {
	t.Helper()
	if got := strings.TrimSpace(runReadmeGitControlled(t, repo, "log", "-1", "--format=%G?")); got != "N" {
		t.Fatalf("README commit signature status = %q, want N", got)
	}
	wantIdentity := readmeBotName + " <" + readmeBotEmail + ">|" + readmeBotName + " <" + readmeBotEmail + ">"
	if got := strings.TrimSpace(runReadmeGitControlled(t, repo, "log", "-1", "--format=%an <%ae>|%cn <%ce>")); got != wantIdentity {
		t.Fatalf("README commit identity = %q, want %q", got, wantIdentity)
	}
	tagRef := "oid-" + projectCommit
	if got := strings.TrimSpace(runReadmeGitControlled(t, repo, "cat-file", "-t", tagRef)); got != "tag" {
		t.Fatalf("%s object type = %q, want annotated tag", tagRef, got)
	}
	if got := strings.TrimSpace(runReadmeGitControlled(t, repo, "for-each-ref", "--format=%(contents:signature)", "refs/tags/"+tagRef)); got != "" {
		t.Fatalf("%s carries a signature: %q", tagRef, got)
	}
	wantTagger := readmeBotName + " <" + readmeBotEmail + ">"
	if got := strings.TrimSpace(runReadmeGitControlled(t, repo, "for-each-ref", "--format=%(taggername) %(taggeremail)", "refs/tags/"+tagRef)); got != wantTagger {
		t.Fatalf("%s tagger = %q, want %q", tagRef, got, wantTagger)
	}
}

func runReadmeGitControlled(t *testing.T, dir string, args ...string) string {
	t.Helper()
	runner := newReadmeGitRunner(dir)
	out, err := runner.run(dir, args...)
	if err != nil {
		t.Fatalf("controlled git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func assertReadmeBotConfigControlled(t *testing.T, repo string) {
	t.Helper()
	want := map[string]string{
		"user.name":       readmeBotName,
		"user.email":      readmeBotEmail,
		"author.name":     readmeBotName,
		"author.email":    readmeBotEmail,
		"committer.name":  readmeBotName,
		"committer.email": readmeBotEmail,
	}
	for key, value := range want {
		got := strings.TrimSpace(runReadmeGitControlled(t, repo, "config", "--local", "--get", key))
		if got != value {
			t.Fatalf("local %s = %q, want %q", key, got, value)
		}
	}
	for _, key := range []string{"commit.gpgSign", "tag.gpgSign"} {
		got := strings.TrimSpace(runReadmeGitControlled(t, repo, "config", "--local", "--get-all", key))
		if got != "false" {
			t.Fatalf("local %s values = %q, want a single false", key, got)
		}
	}
}

func readmeTestConfigArg(args []string, key string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-c" {
			continue
		}
		if strings.HasPrefix(args[i+1], key+"=") {
			return strings.TrimPrefix(args[i+1], key+"=")
		}
	}
	return ""
}

func assertSafeDirs(t *testing.T, args []string, want ...string) {
	t.Helper()
	got := make(map[string]struct{})
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-c" || !strings.HasPrefix(args[i+1], "safe.directory=") {
			continue
		}
		got[strings.TrimPrefix(args[i+1], "safe.directory=")] = struct{}{}
	}
	if len(got) != len(want) {
		t.Fatalf("safe.directory count = %d, want %d in %v", len(got), len(want), args)
	}
	for _, dir := range want {
		if _, ok := got[filepath.Clean(dir)]; !ok {
			t.Fatalf("missing safe.directory=%s in %v", dir, args)
		}
	}
}

func readmeTestEnvValues(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		values[strings.ToUpper(key)] = value
	}
	return values
}
