package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cfgpkg "github.com/tursomari/machtiani/agent/internal/file-discovery/internal/config"
)

type toolCallResult struct {
	tool   string
	rg     *rgCommand
	sed    *sedCommand
	ls     *lsCommand
	reject string
}

type validation func(*testing.T, toolCallResult)

type executionValidation func(*testing.T, []byte, error)

type toolCallTestCase struct {
	name         string
	input        string
	mode         cfgpkg.ToolCallMode
	validators   []validation
	execValidate executionValidation
	skipReason   string
}

func runValidations(t *testing.T, res toolCallResult, validators []validation) {
	t.Helper()
	for _, v := range validators {
		if v == nil {
			continue
		}
		v(t, res)
	}
}

func expectTool(name string) validation {
	return func(t *testing.T, res toolCallResult) {
		t.Helper()
		if res.tool != name {
			t.Fatalf("expected tool %q, got %q (reject=%q)", name, res.tool, res.reject)
		}
	}
}

func expectNoCommands() validation {
	return func(t *testing.T, res toolCallResult) {
		t.Helper()
		if res.rg != nil || res.sed != nil || res.ls != nil {
			t.Fatalf("expected no commands, got rg=%v sed=%v ls=%v", res.rg, res.sed, res.ls)
		}
	}
}

func expectRejectContains(substr string) validation {
	return func(t *testing.T, res toolCallResult) {
		t.Helper()
		if !strings.Contains(res.reject, substr) {
			t.Fatalf("expected reject %q to contain %q", res.reject, substr)
		}
	}
}

func validateRGCommand(kind, pattern string) validation {
	return func(t *testing.T, res toolCallResult) {
		t.Helper()
		if res.tool != "file_search" {
			t.Fatalf("expected tool file_search, got %q (reject=%q)", res.tool, res.reject)
		}
		if res.reject != "" {
			t.Fatalf("unexpected reject for rg: %q", res.reject)
		}
		if res.rg == nil {
			t.Fatalf("expected rg command, got nil")
		}
		if res.rg.kind != kind {
			t.Fatalf("expected rg kind %q, got %q", kind, res.rg.kind)
		}
		if res.rg.pattern != pattern {
			t.Fatalf("expected rg pattern %q, got %q", pattern, res.rg.pattern)
		}
		if res.sed != nil || res.ls != nil {
			t.Fatalf("expected only rg command, got sed=%v ls=%v", res.sed, res.ls)
		}
	}
}

func validateSEDCommand(program, path string) validation {
	return func(t *testing.T, res toolCallResult) {
		t.Helper()
		if res.tool != "read_file" {
			t.Fatalf("expected tool read_file, got %q (reject=%q)", res.tool, res.reject)
		}
		if res.reject != "" {
			t.Fatalf("unexpected reject for sed: %q", res.reject)
		}
		if res.sed == nil {
			t.Fatalf("expected sed command, got nil")
		}
		expectedProgram := strings.TrimSpace(program)
		if res.sed.program != expectedProgram {
			t.Fatalf("expected sed program %q, got %q", expectedProgram, res.sed.program)
		}
		expectedPath := filepath.ToSlash(strings.TrimSpace(path))
		if res.sed.path != expectedPath {
			t.Fatalf("expected sed path %q, got %q", expectedPath, res.sed.path)
		}
		if res.rg != nil || res.ls != nil {
			t.Fatalf("expected only sed command, got rg=%v ls=%v", res.rg, res.ls)
		}
	}
}

func validateLSCommand(path string) validation {
	return func(t *testing.T, res toolCallResult) {
		t.Helper()
		if res.tool != "list_dir" {
			t.Fatalf("expected tool list_dir, got %q (reject=%q)", res.tool, res.reject)
		}
		if res.reject != "" {
			t.Fatalf("unexpected reject for ls: %q", res.reject)
		}
		if res.ls == nil {
			t.Fatalf("expected ls command, got nil")
		}
		expectedPath := filepath.ToSlash(strings.TrimSpace(path))
		if res.ls.path != expectedPath {
			t.Fatalf("expected ls path %q, got %q", expectedPath, res.ls.path)
		}
		if res.rg != nil || res.sed != nil {
			t.Fatalf("expected only ls command, got rg=%v sed=%v", res.rg, res.sed)
		}
	}
}

func execExpectRGPaths(expected ...string) executionValidation {
	return func(t *testing.T, result []byte, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		trimmed := strings.TrimSpace(string(result))
		if len(expected) == 0 {
			if trimmed != "" {
				t.Fatalf("expected empty result, got %q", trimmed)
			}
			return
		}
		if trimmed == "" {
			t.Fatalf("expected non-empty result")
		}
		lines := strings.Split(trimmed, "\n")
		if len(lines) != len(expected) {
			t.Fatalf("expected %d result lines, got %v", len(expected), lines)
		}
		for i, line := range expected {
			if lines[i] != line {
				t.Fatalf("expected line %q at index %d, got %q", line, i, lines[i])
			}
		}
	}
}

func execExpectSedContains(substrings ...string) executionValidation {
	return func(t *testing.T, result []byte, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		out := string(result)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("expected non-empty sed output")
		}
		for _, sub := range substrings {
			if !strings.Contains(out, sub) {
				t.Fatalf("expected sed output to contain %q, got %q", sub, out)
			}
		}
	}
}

func execExpectLSContains(substrings ...string) executionValidation {
	return func(t *testing.T, result []byte, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		out := string(result)
		if strings.TrimSpace(out) == "" {
			t.Fatalf("expected non-empty ls output")
		}
		for _, sub := range substrings {
			if !strings.Contains(out, sub) {
				t.Fatalf("expected ls output to contain %q, got %q", sub, out)
			}
		}
	}
}

var toolCallTestCases = []toolCallTestCase{
	// JSON positive cases
	{
		name:  "json_file_search_basic",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"README\\.md"}}`,
		validators: []validation{
			validateRGCommand("files_pattern", "README\\.md"),
		},
		execValidate: execExpectRGPaths("README.md"),
	},
	{
		name:  "json_file_search_complex_regex",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"ResponseError|\\.d\\.ts"}}`,
		validators: []validation{
			validateRGCommand("files_pattern", "ResponseError|\\.d\\.ts"),
		},
		execValidate: execExpectRGPaths("errors/ResponseError.ts", "test.d.ts"),
	},
	{
		name:  "json_file_search_unicode",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"caf\u00e9|na\u00efve|r\u00e9sum\u00e9"}}`,
		validators: []validation{
			validateRGCommand("files_pattern", "caf\u00e9|na\u00efve|r\u00e9sum\u00e9"),
		},
		execValidate: execExpectRGPaths("docs/r\u00e9sum\u00e9.md"),
	},
	{
		name:  "json_file_search_extra_fields",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"Makefile","limit":5},"trace_id":"abc123"}`,
		validators: []validation{
			validateRGCommand("files_pattern", "Makefile"),
		},
		execValidate: execExpectRGPaths("Makefile"),
	},
	{
		name:  "json_file_search_typescript_pattern",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"(?i)(component|service)\\.tsx$"}}`,
		validators: []validation{
			validateRGCommand("files_pattern", "(?i)(component|service)\\.tsx$"),
		},
		execValidate: execExpectRGPaths("ui/service.tsx"),
	},
	{
		name:  "json_read_file_range",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"read_file","args":{"program":"1,20p","path":"internal/discovery/discovery.go"}}`,
		validators: []validation{
			validateSEDCommand("1,20p", "internal/discovery/discovery.go"),
		},
		execValidate: execExpectSedContains("FixtureSummary", "discovery line"),
	},
	{
		name:  "json_read_file_pattern_with_escape",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"read_file","args":{"program":"/http\\/2.+GOAWAY/p","path":"transport/logs/http2.txt"}}`,
		validators: []validation{
			validateSEDCommand("/http\\/2.+GOAWAY/p", "transport/logs/http2.txt"),
		},
		execValidate: execExpectSedContains("GOAWAY frame"),
	},
	{
		name:  "json_read_file_program_trimmed",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"read_file","args":{"program":"  15,30p  ","path":"  pkg/server/http.go  "}}`,
		validators: []validation{
			validateSEDCommand("15,30p", "pkg/server/http.go"),
		},
		execValidate: execExpectSedContains("server line", "server line 20"),
	},
	{
		name:  "json_read_file_extra_fields",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"read_file","args":{"program":"/TODO/p","path":"docs/readme.md","encoding":"utf-8"}}`,
		validators: []validation{
			validateSEDCommand("/TODO/p", "docs/readme.md"),
		},
		execValidate: execExpectSedContains("TODO: flesh out documentation"),
	},
	{
		name:  "json_list_dir_trimmed_path",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"list_dir","args":{"path":" ./cmd "}}`,
		validators: []validation{
			validateLSCommand("./cmd"),
		},
		execValidate: execExpectLSContains("main.go"),
	},

	// Simple positive cases
	{
		name:  "simple_file_search_basic",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\nkind: files_pattern\npattern: README\n[file-search]",
		validators: []validation{
			validateRGCommand("files_pattern", "README"),
		},
		execValidate: execExpectRGPaths("README.md"),
	},
	{
		name:  "simple_file_search_uppercase_tag",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[FILE-SEARCH]\nkind: files_pattern\npattern: src\\/api\n[FILE-SEARCH]",
		validators: []validation{
			validateRGCommand("files_pattern", "src\\/api"),
		},
		execValidate: execExpectRGPaths("src/api/router.go"),
	},
	{
		name:  "simple_file_search_quoted_pattern",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\nkind: files_pattern\npattern: \"handler:register\"\n[file-search]",
		validators: []validation{
			validateRGCommand("files_pattern", "handler:register"),
		},
		execValidate: execExpectRGPaths("handlers/handler:register.go"),
	},
	{
		name:  "simple_file_search_extra_whitespace",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "  [file-search]\n\tkind: files_pattern\n\tpattern: services\\.yaml\n\n[file-search]  ",
		validators: []validation{
			validateRGCommand("files_pattern", "services\\.yaml"),
		},
		execValidate: execExpectRGPaths("config/services.yaml"),
	},
	{
		name:  "simple_file_search_key_case",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\nKind: files_pattern\npattern: (?i)logger\n[file-search]",
		validators: []validation{
			validateRGCommand("files_pattern", "(?i)logger"),
		},
		execValidate: execExpectRGPaths("internal/logger.go"),
	},
	{
		name:  "simple_read_file_range",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[read-file]\nprogram: 10,40p\npath: internal/app/server.go\n[read-file]",
		validators: []validation{
			validateSEDCommand("10,40p", "internal/app/server.go"),
		},
		execValidate: execExpectSedContains("app server line", "app server line 20"),
	},
	{
		name:  "simple_read_file_regex",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[read-file]\nprogram: /func\\s+Test.+/p\npath: tests/server_test.go\n[read-file]",
		validators: []validation{
			validateSEDCommand("/func\\s+Test.+/p", "tests/server_test.go"),
		},
		skipReason: "TODO: sed BRE lacks \\s support; enable extended regex before executing",
	},
	{
		name:  "simple_read_file_program_trimmed",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[read-file]\nprogram:  5,12p  \npath:   pkg/worker/job.go\n[read-file]",
		validators: []validation{
			validateSEDCommand("5,12p", "pkg/worker/job.go"),
		},
		execValidate: execExpectSedContains("job line", "job line 5"),
	},
	{
		name:  "simple_read_file_path_with_spaces",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[read-file]\nprogram: /TODO/p\npath: docs/spec sheet.txt\n[read-file]",
		validators: []validation{
			validateSEDCommand("/TODO/p", "docs/spec sheet.txt"),
		},
		execValidate: execExpectSedContains("TODO: add acceptance criteria"),
	},
	{
		name:  "simple_list_dir_basic",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[list-dir]\npath: internal/discovery\n[list-dir]",
		validators: []validation{
			validateLSCommand("internal/discovery"),
		},
		execValidate: execExpectLSContains("discovery.go"),
	},

	// JSON negative cases
	{
		name:  "json_invalid_json",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":}}`,
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("invalid JSON for tool call"),
		},
	},
	{
		name:  "json_missing_tool_field",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"args":{"kind":"files_pattern","pattern":"foo"}}`,
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("missing 'tool' field"),
		},
	},
	{
		name:  "json_unknown_tool",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"delete_repo","args":{}}`,
		validators: []validation{
			expectTool("delete_repo"),
			expectNoCommands(),
			expectRejectContains("unknown tool"),
		},
	},
	{
		name:  "json_simple_block_in_json_mode",
		mode:  cfgpkg.ToolCallModeJSON,
		input: "[file-search]\nkind: files_pattern\npattern: foo\n[file-search]",
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("invalid JSON for tool call"),
		},
	},
	{
		name:  "json_file_search_wrong_kind",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"lines","pattern":"foo"}}`,
		validators: []validation{
			expectTool("file_search"),
			expectNoCommands(),
			expectRejectContains("file_search.kind must be 'files_pattern'"),
		},
	},
	{
		name:  "json_file_search_missing_pattern",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"   "}}`,
		validators: []validation{
			expectTool("file_search"),
			expectNoCommands(),
			expectRejectContains("file_search.pattern is required"),
		},
	},
	{
		name:  "json_read_file_invalid_program",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"read_file","args":{"program":"/needle/","path":"README.md"}}`,
		validators: []validation{
			expectTool("read_file"),
			expectNoCommands(),
			expectRejectContains("read_file.program must be"),
		},
	},
	{
		name:  "json_read_file_range_too_large",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"read_file","args":{"program":"1,500p","path":"README.md"}}`,
		validators: []validation{
			expectTool("read_file"),
			expectNoCommands(),
			expectRejectContains("range too large"),
		},
	},
	{
		name:  "json_read_file_invalid_path",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"read_file","args":{"program":"1,5p","path":"../secret.txt"}}`,
		validators: []validation{
			expectTool("read_file"),
			expectNoCommands(),
			expectRejectContains("invalid path"),
		},
	},
	{
		name:  "json_list_dir_invalid_path_absolute",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"list_dir","args":{"path":"/etc"}}`,
		validators: []validation{
			expectTool("list_dir"),
			expectNoCommands(),
			expectRejectContains("invalid path"),
		},
	},

	// Simple negative cases
	{
		name:  "simple_empty_input",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "   \n\t  ",
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("empty content"),
		},
	},
	{
		name:  "simple_missing_closing_tag",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\nkind: files_pattern\npattern: foo",
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("mismatched tool call tags"),
		},
	},
	{
		name:  "simple_mismatched_tags",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\nkind: files_pattern\npattern: foo\n[read-file]",
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("mismatched tool call tags"),
		},
	},
	{
		name:  "simple_unknown_tool",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[scan-repo]\npattern: foo\n[scan-repo]",
		validators: []validation{
			expectTool("scan_repo"),
			expectNoCommands(),
			expectRejectContains("unknown tool"),
		},
	},
	{
		name:  "simple_file_search_missing_kind",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\npattern: foo\n[file-search]",
		validators: []validation{
			expectTool("file_search"),
			expectNoCommands(),
			expectRejectContains("file_search.kind is required"),
		},
	},
	{
		name:  "simple_file_search_missing_pattern",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\nkind: files_pattern\n[file-search]",
		validators: []validation{
			expectTool("file_search"),
			expectNoCommands(),
			expectRejectContains("file_search.pattern is required"),
		},
	},
	{
		name:  "simple_invalid_argument_format",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\nkind files_pattern\npattern: foo\n[file-search]",
		validators: []validation{
			expectTool("file_search"),
			expectNoCommands(),
			expectRejectContains("invalid argument format"),
		},
	},
	{
		name:  "simple_read_file_invalid_program",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[read-file]\nprogram: /needle/\npath: README.md\n[read-file]",
		validators: []validation{
			expectTool("read_file"),
			expectNoCommands(),
			expectRejectContains("read_file.program must be"),
		},
	},
	{
		name:  "simple_read_file_invalid_path",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[read-file]\nprogram: 1,5p\npath: ../secret.txt\n[read-file]",
		validators: []validation{
			expectTool("read_file"),
			expectNoCommands(),
			expectRejectContains("invalid path"),
		},
	},
	{
		name:  "simple_list_dir_invalid_path_absolute",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[list-dir]\npath: /etc\n[list-dir]",
		validators: []validation{
			expectTool("list_dir"),
			expectNoCommands(),
			expectRejectContains("invalid path"),
		},
	},

	// Transcript-derived failure regressions (close-but-invalid inputs observed in production traces).
	{
		name:  "transcript_simple_mismatched_tag_round4_58159",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\npattern: \"ResponseError\"\n[files_pattern]",
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("mismatched tool call tags"),
		},
	},
	{
		name:  "transcript_simple_duplicate_closing_tag_round6_98727",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "[file-search]\nkind: files_pattern\npattern: \"ResponseError\"\n[file-search]\n[file-search]",
		validators: []validation{
			expectTool("file_search"),
			expectNoCommands(),
			expectRejectContains("invalid argument format"),
		},
	},
	{
		name:  "transcript_simple_prefixed_text_round11_98727",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "I will start by searching for ResponseError.\n[file-search]\nkind: files_pattern\npattern: \"ResponseError\"\n[file-search]",
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("invalid JSON for tool call"),
		},
	},
	{
		name:  "transcript_yaml_like_payload_round4_98727",
		mode:  cfgpkg.ToolCallModeSimple,
		input: "file_search:\n  pattern: \"ResponseError.ts\"\nEND_RG_OUT",
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("invalid JSON for tool call"),
		},
	},
	{
		name:  "transcript_json_mode_with_plan_text_round11_98727",
		mode:  cfgpkg.ToolCallModeJSON,
		input: "The user wants to add a missing type...\n[file-search]\nkind: files_pattern\npattern: \"ResponseError|error\\.d\\.ts|error\\.ts\"\n[file-search]",
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("invalid JSON for tool call"),
		},
	},
	{
		name:  "transcript_json_trailing_comma_round23_58159",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"ResponseError",}}`,
		validators: []validation{
			expectTool(""),
			expectNoCommands(),
			expectRejectContains("invalid JSON for tool call"),
		},
	},
	{
		name:  "transcript_json_pretty_responseerror_round23_58159",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{\n  \"tool\": \"file_search\",\n  \"args\": {\n    \"kind\": \"files_pattern\",\n    \"pattern\": \"ResponseError\"\n  }\n}`,
		validators: []validation{
			validateRGCommand("files_pattern", "ResponseError"),
		},
		execValidate: execExpectRGPaths("errors/ResponseError.ts"),
	},
	{
		name:  "transcript_json_missing_kind_round8_58159",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"pattern":"ResponseError"}}`,
		validators: []validation{
			expectTool("file_search"),
			expectNoCommands(),
			expectRejectContains("file_search.kind must be 'files_pattern'"),
		},
	},
	{
		name:  "transcript_json_empty_pattern_round23_98727",
		mode:  cfgpkg.ToolCallModeJSON,
		input: `{"tool":"file_search","args":{"kind":"files_pattern","pattern":"   "}}`,
		validators: []validation{
			expectTool("file_search"),
			expectNoCommands(),
			expectRejectContains("file_search.pattern is required"),
		},
	},
}

func TestToolCallParsing_Comprehensive(t *testing.T) {
	tests := toolCallTestCases

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, rg, sed, ls, reject := parseToolCall(tt.input, tt.mode)
			res := toolCallResult{tool: tool, rg: rg, sed: sed, ls: ls, reject: reject}
			runValidations(t, res, tt.validators)
		})
	}

	if len(tests) < 35 {
		t.Fatalf("expected at least 35 scenarios, got %d", len(tests))
	}
}

func TestToolCallExecution_NativeOperations(t *testing.T) {
	oldRG := runRGFilesFn
	defer func() { runRGFilesFn = oldRG }()
	runRGFilesFn = runRGFiles

	tmpDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.Chdir(origDir)
	}()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}

	createTestFixtures(t)

	for _, tc := range toolCallTestCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tool, rg, sed, ls, reject := parseToolCall(tc.input, tc.mode)
			res := toolCallResult{tool: tool, rg: rg, sed: sed, ls: ls, reject: reject}
			runValidations(t, res, tc.validators)

			if reject != "" {
				if tc.execValidate != nil {
					t.Fatalf("unexpected execValidate for rejecting case: %s", reject)
				}
				return
			}

			if tc.skipReason != "" {
				if tc.execValidate != nil {
					t.Fatalf("skipReason set but execValidate provided")
				}
				t.Skip(tc.skipReason)
			}

			if tc.execValidate == nil {
				if rg != nil || sed != nil || ls != nil {
					t.Fatalf("missing execValidate for successful parse: tool=%q", tool)
				}
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			var (
				result []byte
				err    error
			)

			switch {
			case rg != nil:
				result, err = rg.Execute(ctx)
			case sed != nil:
				result, err = sed.Execute(ctx)
			case ls != nil:
				result, err = ls.Execute(ctx)
			default:
				t.Fatal("no command parsed")
			}

			tc.execValidate(t, result, err)
		})
	}
}

func createTestFixtures(t *testing.T) {
	t.Helper()
	ensureDir := func(path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(path, content string) {
		t.Helper()
		dir := filepath.Dir(path)
		if dir != "." {
			ensureDir(dir)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	numberedLines := func(label string, count int) string {
		var sb strings.Builder
		for i := 1; i <= count; i++ {
			sb.WriteString(fmt.Sprintf("%s %d\n", label, i))
		}
		return sb.String()
	}

	writeFile("README.md", "# Test README\nThis is a test readme file.\n")
	writeFile("Makefile", "all:\n\t@echo building\n")
	writeFile("test.d.ts", "interface ResponseError {\n  message: string;\n}\n")

	var discoverySB strings.Builder
	discoverySB.WriteString("package discovery\n\n")
	discoverySB.WriteString("// Auto-generated fixture for sed range validation.\n")
	discoverySB.WriteString("func FixtureSummary() string {\n\treturn \"discovery fixture\"\n}\n\n")
	discoverySB.WriteString(numberedLines("// discovery line", 25))
	writeFile("internal/discovery/discovery.go", discoverySB.String())

	var testTxt strings.Builder
	for i := 1; i <= 20; i++ {
		testTxt.WriteString(fmt.Sprintf("line %d\n", i))
	}
	writeFile("test.txt", testTxt.String())

	writeFile("transport/logs/http2.txt", "[INFO] http/2.+GOAWAY frame received\n")

	var serverSB strings.Builder
	serverSB.WriteString("package server\n\n")
	serverSB.WriteString("// Handler is a stub used for sed range validation.\n")
	serverSB.WriteString("func Handler() string {\n\treturn \"ok\"\n}\n\n")
	serverSB.WriteString(numberedLines("// server line", 40))
	writeFile("pkg/server/http.go", serverSB.String())

	writeFile("docs/readme.md", "# Docs\n\nTODO: flesh out documentation.\n")

	var workerSB strings.Builder
	workerSB.WriteString("package worker\n\n")
	workerSB.WriteString("// JobRunner is used for sed validation.\n")
	workerSB.WriteString("func JobRunner() error {\n\treturn nil\n}\n\n")
	workerSB.WriteString(numberedLines("// job line", 30))
	writeFile("pkg/worker/job.go", workerSB.String())

	writeFile("docs/spec sheet.txt", "Spec Sheet\n\nTODO: add acceptance criteria.\n")
	writeFile("errors/ResponseError.ts", "export class ResponseError extends Error {}\n")

	var appSB strings.Builder
	appSB.WriteString("package app\n\n")
	appSB.WriteString("// ServerFixture holds lines for sed extraction.\n")
	appSB.WriteString("func ServerFixture() string {\n\treturn \"app fixture\"\n}\n\n")
	appSB.WriteString(numberedLines("// app server line", 45))
	writeFile("internal/app/server.go", appSB.String())

	writeFile("tests/server_test.go", "package tests\n\nimport \"testing\"\n\nfunc TestServer(t *testing.T) {\n\tif false {\n\t\tt.Fatal(\"unreachable\")\n\t}\n}\n")

	writeFile("cmd/main.go", "package main\n\nfunc main() {}\n")
	writeFile("src/api/router.go", "package api\n\nfunc Router() string {\n\treturn \"/health\"\n}\n")
	writeFile("handlers/handler:register.go", "package handlers\n\nfunc Register() {}\n")
	writeFile("config/services.yaml", "services:\n  - name: api\n    port: 8080\n")
	writeFile("ui/service.tsx", "export const Service = () => <div>Service</div>;\n")
	writeFile("internal/logger.go", "package internal\n\n// Logger fixture file\nfunc Logger() {}\n")
	writeFile("docs/r\u00e9sum\u00e9.md", "Résumé content\n")
}
