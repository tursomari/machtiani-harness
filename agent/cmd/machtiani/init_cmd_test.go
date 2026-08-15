package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tursomari/machtiani/agent/internal/llm"
	"github.com/tursomari/machtiani/agent/internal/presentation"
	"github.com/tursomari/machtiani/agent/internal/projectstore"
)

func TestLegacyHandleInitCommandWithDeps(t *testing.T) {
	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(origDir); err != nil {
			t.Fatalf("chdir back: %v", err)
		}
	}()
	llm.ResetConfigForTesting()
	t.Cleanup(llm.ResetConfigForTesting)
	legacyDeps, _, _ := testInitDeps("", "", false)
	handleInitCommand := func(args []string) int {
		return handleInitCommandWithDeps(args, legacyDeps)
	}

	// 1. No flags provided - pflag parses successfully but required flags
	//    are missing, so expects return code 1.
	t.Run("no flags", func(t *testing.T) {
		deps, _, _ := testInitDeps("", "", false)
		code := handleInitCommandWithDeps([]string{}, deps)
		if code != 1 {
			t.Fatalf("expected return code 1, got %d", code)
		}
	})

	// 2. Required flags missing - expects return code 1.
	t.Run("missing required flags", func(t *testing.T) {
		code := handleInitCommand([]string{
			"--provider-url", "https://example.com",
		})
		if code != 1 {
			t.Fatalf("expected return code 1, got %d", code)
		}
	})

	// 3. Successful init - verifies return code 0, config file exists, and
	//    file content contains expected values.
	t.Run("successful init", func(t *testing.T) {
		code := handleInitCommand([]string{
			"--provider-url", "https://api.example.com",
			"--api-key", "sk-test123",
			"--model", "gpt-4o",
			"--reasoning", "high",
		})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		configPath := filepath.Join(".machtiani", "config.toml")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			t.Fatalf("expected config.toml to exist at %s", configPath)
		}

		data, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		content := string(data)

		checks := []string{
			"https://api.example.com",
			"sk-test123",
			"gpt-4o",
			`effort = "high"`,
			`default_model = "default"`,
			`cache_enabled = true`,
			`cache_key_name = "cache_control"`,
			`cache_trigger_threshold = 4096`,
		}
		for _, check := range checks {
			if !strings.Contains(content, check) {
				t.Fatalf("config.toml content missing %q:\n%s", check, content)
			}
		}
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("stat config.toml: %v", err)
		}
		if got := info.Mode().Perm(); got != 0600 {
			t.Fatalf("expected config permissions 0600, got %04o", got)
		}
	})

	// 4. --force flag: config already exists from previous subtest; init
	//    without --force should fail (code 1), then with --force should
	//    succeed (code 0).
	t.Run("force flag", func(t *testing.T) {
		args := []string{
			"--provider-url", "https://api.example.com",
			"--api-key", "sk-test123",
			"--model", "gpt-4o",
		}

		code := handleInitCommand(args)
		if code != 1 {
			t.Fatalf("expected return code 1 for existing config without --force, got %d", code)
		}

		code = handleInitCommand(append(args, "--force"))
		if code != 0 {
			t.Fatalf("expected return code 0 with --force, got %d", code)
		}
	})

	// 5. Custom alias - verifies config contains the alias.
	t.Run("custom alias", func(t *testing.T) {
		code := handleInitCommand([]string{
			"--provider-url", "https://api.example.com",
			"--api-key", "sk-test123",
			"--model", "gpt-4o",
			"--alias", "my-model",
			"--force",
		})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		content := string(data)

		checks := []string{
			`default_model = "my-model"`,
			"my-model",
		}
		for _, check := range checks {
			if !strings.Contains(content, check) {
				t.Fatalf("config.toml content missing %q:\n%s", check, content)
			}
		}
	})

	// 6. parses with LoadGlobalConfig - verifies that the generated config can
	//    be parsed by LoadGlobalConfig and contains expected values.
	t.Run("parses with LoadGlobalConfig", func(t *testing.T) {
		llm.ResetConfigForTesting()

		code := handleInitCommand([]string{
			"--provider-url", "https://api.example.com",
			"--api-key", "sk-test123",
			"--model", "gpt-4o",
			"--force",
		})
		if code != 0 {
			t.Fatalf("expected return code 0, got %d", code)
		}

		if err := os.Chdir(".machtiani"); err != nil {
			t.Fatalf("chdir .machtiani: %v", err)
		}
		defer func() {
			if err := os.Chdir(tmpDir); err != nil {
				t.Fatalf("chdir back to tmpDir: %v", err)
			}
		}()

		cfg, _, err := llm.LoadGlobalConfig()
		if err != nil {
			t.Fatalf("LoadGlobalConfig: %v", err)
		}

		if cfg.DefaultModel != "default" {
			t.Fatalf("expected DefaultModel 'default', got %q", cfg.DefaultModel)
		}

		if cfg.Providers == nil {
			t.Fatal("expected Providers to be non-nil")
		}
		prov, ok := cfg.Providers["default"]
		if !ok {
			t.Fatal("expected Providers to have key 'default'")
		}
		if prov.BaseURL != "https://api.example.com" {
			t.Fatalf("expected Providers['default'].BaseURL 'https://api.example.com', got %q", prov.BaseURL)
		}

		if cfg.Models == nil {
			t.Fatal("expected Models to be non-nil")
		}
		modelDef, ok := cfg.Models["default"]
		if !ok {
			t.Fatal("expected Models to have key 'default'")
		}
		if modelDef.Model != "gpt-4o" {
			t.Fatalf("expected Models['default'].Model 'gpt-4o', got %q", modelDef.Model)
		}
		if len(modelDef.Params) != 0 {
			t.Fatalf("expected provider-default reasoning to omit params, got %#v", modelDef.Params)
		}

		llm.ResetConfigForTesting()
	})
}

func TestInitStartsNewConfigurationWizard(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	_, stderr := captureOutput(func() {
		if code := handleInitCommand(nil); code != 1 {
			t.Errorf("init exit = %d, want 1 without a terminal", code)
		}
	})
	if !strings.Contains(stderr, "interactive configuration requires a terminal") {
		t.Fatalf("stderr = %q", stderr)
	}
	if strings.Contains(stderr, "provider-url") {
		t.Fatalf("init exposed the legacy wizard: %q", stderr)
	}
}

func TestInitPreservesExistingConfigurationAndSyncsModes(t *testing.T) {
	_, cleanup := setupConfigTest(t)
	defer cleanup()
	if code := handleConfigAddCommand([]string{
		"--provider", "p", "--url", "https://example.com/v1", "--api-key-env", "TEST_API_KEY",
		"--model", "one", "--alias", "one", "--no-interactive",
	}); code != 0 {
		t.Fatalf("config add exit = %d", code)
	}
	path := filepath.Join(".machtiani", "config.toml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := captureOutput(func() {
		if code := handleInitCommand([]string{"--no-interactive"}); code != 0 {
			t.Errorf("init exit = %d, want 0", code)
		}
	})
	if stderr != "" || !strings.Contains(stdout, "canonical modes synchronized") {
		t.Fatalf("stdout = %q, stderr = %q", stdout, stderr)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("init changed an existing configuration")
	}
}

func TestInitRejectsUnknownLegacyProviderURLFlag(t *testing.T) {
	captureOutput(func() {
		if code := handleInitCommand([]string{"--provider-url", "https://example.com"}); code != 2 {
			t.Errorf("init exit = %d, want 2", code)
		}
	})
}

func TestPromptInitConfigScopeRecommendsGlobal(t *testing.T) {
	var out bytes.Buffer
	useGlobal, err := promptInitConfigScope(bufio.NewReader(strings.NewReader("\n")), &out)
	if err != nil {
		t.Fatal(err)
	}
	if !useGlobal {
		t.Fatal("default scope was not global")
	}
	for _, want := range []string{
		"Configuration scope",
		"Global shares providers and models across projects. Sessions remain project-specific.",
		"Use global config? [Y/n] (recommended):",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("prompt output %q does not contain %q", out.String(), want)
		}
	}
}

func TestInitNonInteractiveCreatesStableUUIDHomeStore(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MACHTIANI_CONFIG", "")

	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })

	global := filepath.Join(home, ".machtiani", "config.toml")
	if code := handleConfigAddCommand([]string{
		"--global", "--provider", "p", "--url", "https://example.com/v1",
		"--api-key-env", "TEST_API_KEY", "--model", "one", "--alias", "one",
		"--no-interactive",
	}); code != 0 {
		t.Fatalf("config add exit = %d", code)
	}

	if code := handleInitCommand([]string{"--no-interactive"}); code != 0 {
		t.Fatalf("init exit = %d", code)
	}
	ctx, err := projectstore.Discover(project)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Status != projectstore.StatusInitialized || ctx.ConfigScope != projectstore.ScopeGlobal {
		t.Fatalf("context = %#v", ctx)
	}
	if _, err := os.Stat(global); err != nil {
		t.Fatalf("global config: %v", err)
	}
	if _, err := os.Stat(ctx.ProjectConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("default init created project config: %v", err)
	}
	for _, path := range []string{ctx.SessionsRoot(), ctx.ArtifactsRoot(), ctx.ScratchRoot(), ctx.MetaRoot()} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("layout path %s: info=%v err=%v", path, info, err)
		}
	}

	firstID := ctx.ID
	if code := handleInitCommand([]string{"--no-interactive"}); code != 0 {
		t.Fatalf("second init exit = %d", code)
	}
	ctx, err = projectstore.Discover(project)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.ID != firstID {
		t.Fatalf("re-init changed UUID from %s to %s", firstID, ctx.ID)
	}
}

func TestHandleInitCommandInteractiveWizard(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, stdout, stderr := testInitDeps("\nhttps://api.example.com/v1\ngpt-5\n\n\n", "sk-hidden", true)
		code := handleInitCommandWithDeps(nil, deps)
		if code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		content := string(data)
		for _, expected := range []string{
			`base_url = "https://api.example.com/v1"`,
			`api_key = "sk-hidden"`,
			`model = "gpt-5"`,
			`default_model = "default"`,
			`cache_enabled = true`,
			`cache_control`,
		} {
			if !strings.Contains(content, expected) {
				t.Fatalf("config missing %q:\n%s", expected, content)
			}
		}
		if strings.Contains(content, "effort =") {
			t.Fatalf("provider default should omit reasoning effort:\n%s", content)
		}
		if strings.Contains(stdout.String(), "sk-hidden") {
			t.Fatalf("wizard echoed API key: %s", stdout.String())
		}
		if !strings.Contains(stdout.String(), "Enable caching? [Y/n]") {
			t.Fatalf("wizard did not show caching prompt: %s", stdout.String())
		}
	})
}

func TestHandleInitCommandInteractiveDeclinesCache(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, _, stderr := testInitDepsWithReasoning("\nhttps://api.example.com/v1\ngpt-5\nwork\nmaybe\nNo\n", "sk-hidden", true, "high")
		code := handleInitCommandWithDeps(nil, deps)
		if code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, `cache_enabled = false`) {
			t.Fatalf("expected caching to be disabled:\n%s", content)
		}
		if strings.Contains(content, "cache_key_name") {
			t.Fatalf("disabled defaults should omit cache payload:\n%s", content)
		}
		if !strings.Contains(content, `effort = "high"`) {
			t.Fatalf("expected selected reasoning effort:\n%s", content)
		}
	})
}

func TestHandleInitCommandNoCacheFlag(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, stdout, stderr := testInitDeps("\nhttps://api.example.com/v1\ngpt-5\n\n", "sk-hidden", true)
		code := handleInitCommandWithDeps([]string{"--no-cache"}, deps)
		if code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "disabled by --no-cache") {
			t.Fatalf("expected no-cache notice: %s", stdout.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		if !strings.Contains(string(data), `cache_enabled = false`) {
			t.Fatalf("expected caching disabled:\n%s", data)
		}
	})
}

func TestHandleInitCommandInteractiveEOFLeavesNoConfig(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, _, _ := testInitDeps("\nhttps://api.example.com/v1\n", "sk-hidden", true)
		code := handleInitCommandWithDeps(nil, deps)
		if code != 1 {
			t.Fatalf("expected return code 1, got %d", code)
		}
		if _, err := os.Stat(filepath.Join(".machtiani", "config.toml")); !os.IsNotExist(err) {
			t.Fatalf("expected no config after EOF, got %v", err)
		}
	})
}

func TestHandleInitCommandFlaggedNoCache(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, _, stderr := testInitDeps("", "", false)
		code := handleInitCommandWithDeps([]string{
			"--provider-url", "https://api.example.com/v1",
			"--api-key", "sk-test",
			"--model", "gpt-5",
			"--no-cache",
		}, deps)
		if code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		if !strings.Contains(string(data), `cache_enabled = false`) || strings.Contains(string(data), "cache_key_name") {
			t.Fatalf("unexpected no-cache config:\n%s", data)
		}
	})
}

func TestReasoningArrowMenu(t *testing.T) {
	input := strings.NewReader("\x1b[B\x1b[B\x1b[B\x1b[B\r")
	out := &bytes.Buffer{}
	got, err := runReasoningMenu(input, out)
	if err != nil {
		t.Fatalf("runReasoningMenu: %v", err)
	}
	if got != "other" {
		t.Fatalf("expected Other selection, got %q", got)
	}
	for _, label := range []string{"Provider default", "Low", "Medium", "High", "Other..."} {
		if !strings.Contains(out.String(), label) {
			t.Fatalf("menu output missing %q: %q", label, out.String())
		}
	}
}

func TestHandleInitCommandInteractiveAddsModelToProvider(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, stdout, stderr := testInitDeps("\nhttps://api.one.example/v1\nmodel-one\nprimary\nmodel-two\nfast\n\n", "sk-one", true)
		steps := []string{"model", "finish"}
		deps.selectNextStep = func(io.Reader, io.Writer, int, string) (string, error) {
			step := steps[0]
			steps = steps[1:]
			return step, nil
		}
		deps.selectDefault = func(_ io.Reader, _ io.Writer, _ int, models []initModel) (string, error) {
			if len(models) != 2 {
				t.Fatalf("default selector received %d models", len(models))
			}
			return "fast", nil
		}

		if code := handleInitCommandWithDeps(nil, deps); code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		content := string(data)
		for _, expected := range []string{
			`default_model = "fast"`,
			`[providers.default]`,
			`[models.primary]`,
			`[models.fast]`,
			`model = "model-one"`,
			`model = "model-two"`,
		} {
			if !strings.Contains(content, expected) {
				t.Fatalf("config missing %q:\n%s", expected, content)
			}
		}
		if got := strings.Count(content, "[providers."); got != 1 {
			t.Fatalf("expected one provider table, got %d:\n%s", got, content)
		}
		if !strings.Contains(stdout.String(), "Models: 2") {
			t.Fatalf("summary did not report two models: %s", stdout.String())
		}
	})
}

func TestHandleInitCommandInteractiveAddsProvider(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, stdout, stderr := testInitDeps("\nhttps://api.one.example/v1\nmodel-one\nprimary\nother\nhttps://api.two.example/v1\nmodel-two\nsecondary\n\n", "", true)
		passwords := []string{"sk-one", "sk-two"}
		deps.readPassword = func(int) ([]byte, error) {
			password := passwords[0]
			passwords = passwords[1:]
			return []byte(password), nil
		}
		steps := []string{"provider", "finish"}
		deps.selectNextStep = func(io.Reader, io.Writer, int, string) (string, error) {
			step := steps[0]
			steps = steps[1:]
			return step, nil
		}
		deps.selectDefault = func(io.Reader, io.Writer, int, []initModel) (string, error) {
			return "secondary", nil
		}

		if code := handleInitCommandWithDeps(nil, deps); code != 0 {
			t.Fatalf("expected return code 0, got %d; stderr=%s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		content := string(data)
		for _, expected := range []string{
			`default_model = "secondary"`,
			`[providers.default]`,
			`[providers.other]`,
			`api_key = "sk-one"`,
			`api_key = "sk-two"`,
			`provider = "default"`,
			`provider = "other"`,
		} {
			if !strings.Contains(content, expected) {
				t.Fatalf("config missing %q:\n%s", expected, content)
			}
		}
		if !strings.Contains(stdout.String(), "Providers: 2") {
			t.Fatalf("summary did not report two providers: %s", stdout.String())
		}
	})
}

func TestInitNextStepArrowMenu(t *testing.T) {
	out := &bytes.Buffer{}
	got, err := runInitMenu(strings.NewReader("\x1b[B\r"), out, "What would you like to do?", "Use arrows.", []initMenuOption{
		{label: "Finish setup", value: "finish"},
		{label: "Add another model", value: "model"},
		{label: "Add another provider", value: "provider"},
	})
	if err != nil {
		t.Fatalf("runInitMenu: %v", err)
	}
	if got != "model" {
		t.Fatalf("expected model selection, got %q", got)
	}
	if !strings.Contains(out.String(), "\x1b[2J\x1b[H") {
		t.Fatal("expected selecting a menu action to clear the previous view")
	}
}

func TestInitMenuSemanticColors(t *testing.T) {
	tests := []struct {
		name         string
		theme        presentation.Theme
		wantSelected string
		wantFinish   string
	}{
		{
			name:         "terminal palette",
			theme:        presentation.NewForTest(presentation.ProfileTerminal, true, false),
			wantSelected: "\x1b[1;36m→ Manage models\x1b[0m",
			wantFinish:   "  \x1b[32mFinish\x1b[0m",
		},
		{
			name:         "dark palette",
			theme:        presentation.NewForTest(presentation.ProfileMachtianiDark, true, true),
			wantSelected: "\x1b[1;38;2;88;199;217m→ Manage models\x1b[0m",
			wantFinish:   "  \x1b[38;2;116;201;145mFinish\x1b[0m",
		},
		{
			name:         "light palette",
			theme:        presentation.NewForTest(presentation.ProfileMachtianiLight, true, true),
			wantSelected: "\x1b[1;38;2;0;107;120m→ Manage models\x1b[0m",
			wantFinish:   "  \x1b[38;2;34;107;58mFinish\x1b[0m",
		},
		{
			name:         "color disabled",
			theme:        presentation.NewForTest(presentation.ProfileTerminal, false, false),
			wantSelected: "\x1b[1m→ Manage models\x1b[0m",
			wantFinish:   "  Finish",
		},
	}

	options := []initMenuOption{
		{label: "Finish", value: "finish"},
		{label: "Manage models", value: "model"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			renderInitMenuOptions(&out, options, 1, false, test.theme)
			got := out.String()
			if !strings.Contains(got, test.wantSelected) {
				t.Fatalf("selected option did not use selection styling\nwant substring: %q\noutput: %q", test.wantSelected, got)
			}
			if !strings.Contains(got, test.wantFinish) {
				t.Fatalf("unselected Finish did not use its semantic styling\nwant substring: %q\noutput: %q", test.wantFinish, got)
			}
		})
	}
}

func TestSelectedFinishUsesOrdinarySelectionColor(t *testing.T) {
	theme := presentation.NewForTest(presentation.ProfileTerminal, true, false)
	var out bytes.Buffer
	renderInitMenuOptions(&out, []initMenuOption{
		{label: "Finish", value: "finish"},
		{label: "Manage models", value: "model"},
	}, 0, false, theme)

	got := out.String()
	if !strings.Contains(got, "\x1b[1;36m→ Finish\x1b[0m") {
		t.Fatalf("selected Finish should use ordinary selection styling: %q", got)
	}
	if strings.Contains(got, "\x1b[32mFinish") {
		t.Fatalf("selected Finish retained its unselected color: %q", got)
	}
}

func TestMenuSelectionUsesASCIIGlyphMode(t *testing.T) {
	theme := presentation.NewForTestWithGlyphs(presentation.ProfileTerminal, presentation.GlyphASCII, false, false)
	var out bytes.Buffer
	renderInitMenuOptions(&out, []initMenuOption{{label: "Manage models", value: "model"}}, 0, false, theme)
	if got := out.String(); !strings.Contains(got, "-> Manage models") || strings.Contains(got, "→") {
		t.Fatalf("ASCII selection marker mismatch: %q", got)
	}
}

func TestConfigDocumentMenuThemeUsesSelectedConfigAndEnvironmentOverride(t *testing.T) {
	doc := &configDocument{raw: map[string]any{
		"ui": map[string]any{"theme": "machtiani-dark"},
	}}

	theme, err := doc.menuTheme(&bytes.Buffer{})
	if err != nil {
		t.Fatalf("menuTheme: %v", err)
	}
	if theme.Profile != presentation.ProfileMachtianiDark {
		t.Fatalf("profile = %q, want %q", theme.Profile, presentation.ProfileMachtianiDark)
	}

	t.Setenv("MACHTIANI_THEME", "machtiani-light")
	theme, err = doc.menuTheme(&bytes.Buffer{})
	if err != nil {
		t.Fatalf("menuTheme with override: %v", err)
	}
	if theme.Profile != presentation.ProfileMachtianiLight {
		t.Fatalf("overridden profile = %q, want %q", theme.Profile, presentation.ProfileMachtianiLight)
	}
}

func TestPromptOtherReasoning(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "known xhigh", input: "xhigh\n", want: "xhigh"},
		{name: "known max", input: "MAX\n", want: "max"},
		{name: "correct typo", input: "xhig\n\n", want: "xhigh"},
		{name: "confirm provider value", input: "minimal\ny\n", want: "minimal"},
		{name: "provider default", input: "\n", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := &bytes.Buffer{}
			got, err := promptOtherReasoning(bufio.NewReader(strings.NewReader(test.input)), out)
			if err != nil {
				t.Fatalf("promptOtherReasoning: %v", err)
			}
			if got != test.want {
				t.Fatalf("got %q, want %q; output=%s", got, test.want, out.String())
			}
		})
	}
}

func TestHandleInitCommandRejectsLikelyReasoningTypo(t *testing.T) {
	withInitWorkingDir(t, func() {
		deps, _, stderr := testInitDeps("", "", false)
		code := handleInitCommandWithDeps([]string{
			"--provider-url", "https://api.example.com/v1",
			"--api-key", "sk-test",
			"--model", "gpt-5",
			"--reasoning", "xhig",
		}, deps)
		if code != 1 || !strings.Contains(stderr.String(), `did you mean "xhigh"`) {
			t.Fatalf("expected typo suggestion, code=%d stderr=%s", code, stderr.String())
		}
	})
}

func TestHandleInitCommandAcceptsExplicitProviderReasoning(t *testing.T) {
	for _, reasoning := range []string{"xhigh", "max", "provider-special"} {
		t.Run(reasoning, func(t *testing.T) {
			withInitWorkingDir(t, func() {
				deps, _, stderr := testInitDeps("", "", false)
				code := handleInitCommandWithDeps([]string{
					"--provider-url", "https://api.example.com/v1",
					"--api-key", "sk-test",
					"--model", "gpt-5",
					"--reasoning", reasoning,
				}, deps)
				if code != 0 {
					t.Fatalf("expected reasoning %q to pass, code=%d stderr=%s", reasoning, code, stderr.String())
				}
				data, err := os.ReadFile(filepath.Join(".machtiani", "config.toml"))
				if err != nil {
					t.Fatalf("read generated config: %v", err)
				}
				if !strings.Contains(string(data), fmt.Sprintf(`effort = %q`, reasoning)) {
					t.Fatalf("reasoning value missing from config:\n%s", data)
				}
			})
		})
	}
}

func TestReasoningTypoSuggestion(t *testing.T) {
	for input, want := range map[string]string{"xhig": "xhigh", "xhihg": "xhigh", "mx": "max"} {
		got, ok := reasoningTypoSuggestion(input)
		if !ok || got != want {
			t.Fatalf("reasoningTypoSuggestion(%q) = %q, %t; want %q, true", input, got, ok, want)
		}
	}
	for _, input := range []string{"xhigh", "max", "minimal", "provider-special"} {
		if got, ok := reasoningTypoSuggestion(input); ok {
			t.Fatalf("reasoningTypoSuggestion(%q) unexpectedly suggested %q", input, got)
		}
	}
}

func testInitDeps(input, password string, terminal bool) (initCommandDeps, *bytes.Buffer, *bytes.Buffer) {
	return testInitDepsWithReasoning(input, password, terminal, "")
}

func testInitDepsWithReasoning(input, password string, terminal bool, reasoning string) (initCommandDeps, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return initCommandDeps{
		in:         strings.NewReader(input),
		out:        stdout,
		errOut:     stderr,
		stdinFD:    0,
		isTerminal: func(int) bool { return terminal },
		readPassword: func(int) ([]byte, error) {
			if password == "" {
				return nil, io.EOF
			}
			return []byte(password), nil
		},
		selectReasoning: func(io.Reader, io.Writer, int) (string, error) {
			return reasoning, nil
		},
		selectNextStep: func(io.Reader, io.Writer, int, string) (string, error) {
			return "finish", nil
		},
		selectDefault: func(_ io.Reader, _ io.Writer, _ int, models []initModel) (string, error) {
			return models[0].alias, nil
		},
	}, stdout, stderr
}

func withInitWorkingDir(t *testing.T, fn func()) {
	t.Helper()
	original, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	}()
	llm.ResetConfigForTesting()
	defer llm.ResetConfigForTesting()
	fn()
}
