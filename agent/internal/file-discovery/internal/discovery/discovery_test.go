package discovery

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
)

func TestApplyExcludes(t *testing.T) {
	in := []string{
		".git/keep",
		"node_modules/pkg/index.js",
		"assets/logo.png",
		"docs/spec.pdf",
		"README.md",
		filepath.FromSlash("cmd/main.go"),
		filepath.FromSlash("internal/x.go"),
	}
	out := applyExcludes(in)
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, ".git/keep") {
		t.Fatalf("expected .git excluded; got %v", out)
	}
	if strings.Contains(joined, "node_modules/") {
		t.Fatalf("expected node_modules excluded; got %v", out)
	}
	if strings.Contains(joined, "logo.png") || strings.Contains(joined, "spec.pdf") {
		t.Fatalf("expected binary assets excluded; got %v", out)
	}
	for _, must := range []string{"README.md", "cmd/main.go", "internal/x.go"} {
		if !strings.Contains(joined, must) {
			t.Fatalf("expected %s present; got %v", must, out)
		}
	}
}

func TestApplyPattern(t *testing.T) {
	in := []string{"README.md", "cmd/main.go", "internal/x.go"}
	out, err := applyPattern(in, `go|README`)
	if err != nil {
		t.Fatalf("applyPattern error: %v", err)
	}
	got := strings.Join(out, ",")
	if !strings.Contains(got, "README.md") || !strings.Contains(got, "cmd/main.go") || !strings.Contains(got, "internal/x.go") {
		t.Fatalf("pattern filtering unexpected: %v", out)
	}
	if _, err := applyPattern(in, "["); err == nil {
		t.Fatalf("expected regex compile error")
	}
}

func TestFormatRGOut_TruncationFooter(t *testing.T) {
	paths := []string{"a", "b", "c"}
	block, n, truncated, lines := formatRGOut(paths, 1) // force truncation
	if !truncated {
		t.Fatalf("expected truncation")
	}
	if n <= 0 || lines <= 0 {
		t.Fatalf("expected non-zero bytes and line counts; got n=%d lines=%d", n, lines)
	}
	if !strings.Contains(block, "RG_OUT:") || !strings.Contains(block, "END_RG_OUT") {
		t.Fatalf("missing RG_OUT markers: %q", block)
	}
	if !strings.Contains(block, "[TRUNCATED:") {
		t.Fatalf("expected truncation footer; got:\n%s", block)
	}
}

func TestValidateAndNormalizeFinalBlock(t *testing.T) {
	raw := "BEGIN_RELEVANT_FILES[file-discovery]\n README.md \ncmd/main.go\nEND_RELEVANT_FILES[file-discovery]\n"
	norm, err := validateAndNormalizeFinalBlock(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(norm, "BEGIN_RELEVANT_FILES[file-discovery]") || !strings.Contains(norm, "END_RELEVANT_FILES[file-discovery]") {
		t.Fatalf("normalized block missing markers: %q", norm)
	}
	if _, err := validateAndNormalizeFinalBlock("no markers here"); err == nil {
		t.Fatalf("expected error for missing block")
	}
	// invalid path
	bad := "BEGIN_RELEVANT_FILES[file-discovery]\n../secret\nEND_RELEVANT_FILES[file-discovery]\n"
	if _, err := validateAndNormalizeFinalBlock(bad); err == nil {
		t.Fatalf("expected error for invalid path")
	}
}

func TestRunRGFilesFnOverride_Unit(t *testing.T) {
	// Override the runner to ensure the hook compiles and can be swapped in tests
	old := runRGFilesFn
	defer func() { runRGFilesFn = old }()
	runRGFilesFn = func(ctx context.Context) ([]string, rgStats, error) {
		return []string{"README.md"}, rgStats{totalLines: 1}, nil
	}
	// Call through the dry-run path indirectly by mimicking usage of applyExcludes
	out := applyExcludes([]string{"README.md"})
	if len(out) != 1 || out[0] != "README.md" {
		t.Fatalf("unexpected output: %v", out)
	}
	_ = os.Setenv("__unused__", "1") // keep lints calm about unused imports
}

func TestValidateRelPath(t *testing.T) {
	for _, bad := range []string{"/abs", "../up", "dir/", `bad\\sep`} {
		if err := validateRelPath(bad); err == nil {
			t.Fatalf("expected error for bad path: %q", bad)
		}
	}
	if err := validateRelPath("ok/rel.txt"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatSEDOut_TruncationFooter(t *testing.T) {
	// Create 250 lines; expect 200 emitted with footer
	var lines []string
	for i := 0; i < 250; i++ {
		lines = append(lines, "x")
	}
	block, n, truncated := formatSEDOut("README.md", lines, 200)
	if !truncated || n != 200 {
		t.Fatalf("expected truncation to 200 lines; got truncated=%v n=%d", truncated, n)
	}
	if !strings.Contains(block, "SED_OUT[README.md]:\n") || !strings.Contains(block, "END_SED_OUT") {
		t.Fatalf("missing SED_OUT markers: %q", block)
	}
	if !strings.Contains(block, "[TRUNCATED: 200 lines limit]") {
		t.Fatalf("expected truncation footer; got:\n%s", block)
	}
}

func TestFormatLSOut_TruncationFooter(t *testing.T) {
	// Create 250 lines; expect 200 emitted with footer
	var lines []string
	for i := 0; i < 250; i++ {
		lines = append(lines, "x")
	}
	block, n, truncated := formatLSOut(".", lines, 200)
	if !truncated || n != 200 {
		t.Fatalf("expected truncation to 200 lines; got truncated=%v n=%d", truncated, n)
	}
	if !strings.Contains(block, "LS_OUT[.:\n") && !strings.Contains(block, "LS_OUT[.]:\n") {
		t.Fatalf("missing LS_OUT marker: %q", block)
	}
	if !strings.Contains(block, "END_LS_OUT") {
		t.Fatalf("missing END_LS_OUT marker: %q", block)
	}
	if !strings.Contains(block, "[TRUNCATED: 200 lines limit]") {
		t.Fatalf("expected truncation footer; got:\n%s", block)
	}
}

func TestParseRGAndLSCommands(t *testing.T) {
	// Invalid JSON should be rejected
	tool, rg, sed, ls, reject := parseToolCall("not json", cfgpkg.ToolCallModeJSON)
	if reject == "" || tool != "" || rg != nil || sed != nil || ls != nil {
		t.Fatalf("expected rejection for invalid JSON; got tool=%q rg=%v sed=%v ls=%v reject=%q", tool, rg, sed, ls, reject)
	}
	// Patterned file_search should be accepted
	js := `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"foo"}}`
	tool, rg, sed, ls, reject = parseToolCall(js, cfgpkg.ToolCallModeJSON)
	if reject != "" || tool != "file_search" || rg == nil || rg.kind != "files_pattern" || rg.pattern != "foo" || sed != nil || ls != nil {
		t.Fatalf("expected valid file_search; got tool=%q rg=%v sed=%v ls=%v reject=%q", tool, rg, sed, ls, reject)
	}
	// list_dir valid path
	js = `{"tool":"list_dir","args":{"path":"internal"}}`
	tool, rg, sed, ls, reject = parseToolCall(js, cfgpkg.ToolCallModeJSON)
	if reject != "" || tool != "list_dir" || ls == nil || ls.path != filepath.ToSlash("internal") {
		t.Fatalf("expected list_dir parsed; tool=%q ls=%v reject=%q", tool, ls, reject)
	}
	// list_dir invalid path
	js = `{"tool":"list_dir","args":{"path":"../secret"}}`
	tool, rg, sed, ls, reject = parseToolCall(js, cfgpkg.ToolCallModeJSON)
	if tool != "list_dir" || ls != nil || reject == "" {
		t.Fatalf("expected list_dir invalid rejected; tool=%q ls=%v reject=%q", tool, ls, reject)
	}

	// Bracket syntax accepted in simple mode
	simple := "[file-search]\nkind: files_pattern\npattern: \"foo\"\n[file-search]"
	tool, rg, sed, ls, reject = parseToolCall(simple, cfgpkg.ToolCallModeSimple)
	if reject != "" || tool != "file_search" || rg == nil || rg.pattern != "foo" {
		t.Fatalf("expected bracket file_search accepted; tool=%q rg=%v reject=%q", tool, rg, reject)
	}
	// Bracket syntax rejected in JSON mode
	tool, rg, sed, ls, reject = parseToolCall(simple, cfgpkg.ToolCallModeJSON)
	if reject == "" || tool != "" || rg != nil || sed != nil || ls != nil {
		t.Fatalf("expected bracket call rejected in JSON mode; tool=%q reject=%q", tool, reject)
	}
}

func TestParseSEDCommand_Validation(t *testing.T) {
	// Valid range
	js := `{"tool":"read_file","args":{"program":"1,120p","path":"README.md"}}`
	tool, _, sed, _, reject := parseToolCall(js, cfgpkg.ToolCallModeJSON)
	if reject != "" || tool != "read_file" || sed == nil || sed.program != "1,120p" || sed.path != filepath.ToSlash("README.md") {
		t.Fatalf("expected valid read_file range; tool=%q sed=%v reject=%q", tool, sed, reject)
	}
	// Invalid range (too many lines)
	js = `{"tool":"read_file","args":{"program":"1,500p","path":"README.md"}}`
	tool, _, sed, _, reject = parseToolCall(js, cfgpkg.ToolCallModeJSON)
	if tool != "read_file" || sed != nil || reject == "" {
		t.Fatalf("expected reject for too-large range; tool=%q sed=%v reject=%q", tool, sed, reject)
	}
	// Valid pattern
	js = `{"tool":"read_file","args":{"program":"/hello/p","path":"path/to/file.go"}}`
	tool, _, sed, _, reject = parseToolCall(js, cfgpkg.ToolCallModeJSON)
	if reject != "" || tool != "read_file" || sed == nil || sed.program != "/hello/p" || sed.path != filepath.ToSlash("path/to/file.go") {
		t.Fatalf("expected valid read_file pattern; tool=%q sed=%v reject=%q", tool, sed, reject)
	}
	// Bracket syntax for read_file accepted in simple mode
	simple := "[read-file]\nprogram: /hello/p\npath: path/to/file.go\n[read-file]"
	tool, _, sed, _, reject = parseToolCall(simple, cfgpkg.ToolCallModeSimple)
	if reject != "" || tool != "read_file" || sed == nil || sed.program != "/hello/p" || sed.path != filepath.ToSlash("path/to/file.go") {
		t.Fatalf("expected bracket read_file accepted; tool=%q sed=%v reject=%q", tool, sed, reject)
	}
	// Invalid path
	js = `{"tool":"read_file","args":{"program":"1,10p","path":"../secret.txt"}}`
	tool, _, sed, _, reject = parseToolCall(js, cfgpkg.ToolCallModeJSON)
	if tool != "read_file" || sed != nil || reject == "" {
		t.Fatalf("expected reject for invalid path; tool=%q sed=%v reject=%q", tool, sed, reject)
	}
}

func TestSEDFlow_Smoke(t *testing.T) {
	if _, err := exec.LookPath("sed"); err != nil {
		t.Skip("sed not in PATH; skipping SED smoke test")
	}
	dir := t.TempDir()
	oldCwd, _ := os.Getwd()
	defer os.Chdir(oldCwd)
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	// Prepare a small file
	content := strings.Join([]string{"one", "two", "three", "four", "five", "needle here", "seven"}, "\n") + "\n"
	if err := os.WriteFile("sample.txt", []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Simulate an RG_OUT that included the file
	block, _, _, _ := formatRGOut([]string{"sample.txt"}, 200000)
	paths := collectPathsFromRGBlock(block)
	if len(paths) != 1 || paths[0] != "sample.txt" {
		t.Fatalf("unexpected collect paths: %v", paths)
	}

	// Range program
	lines, _, err := runSed(context.Background(), "1,3p", "sample.txt")
	if err != nil {
		t.Fatalf("runSed range: %v", err)
	}
	sedOut, n, truncated := formatSEDOut("sample.txt", lines, 200)
	if truncated || n != 3 || !strings.Contains(sedOut, "SED_OUT[sample.txt]:\n") || !strings.Contains(sedOut, "END_SED_OUT") {
		t.Fatalf("unexpected sedOut for range; n=%d truncated=%v\n%s", n, truncated, sedOut)
	}

	// Pattern program
	lines2, _, err := runSed(context.Background(), "/needle/p", "sample.txt")
	if err != nil {
		t.Fatalf("runSed pattern: %v", err)
	}
	if len(lines2) != 1 || !strings.Contains(lines2[0], "needle") {
		t.Fatalf("expected one matching line; got %v", lines2)
	}
}
