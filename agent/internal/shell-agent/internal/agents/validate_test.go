package agents

import (
	"testing"
)

func TestTranslateGrepToRg(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectedOutput string
		expectedOk     bool
	}{
		// Positive cases: grep -r from root
		{
			name:           "BasicRNI",
			input:          "grep -rnI pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "BasicRN",
			input:          "grep -rn pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "BasicR",
			input:          "grep -r pattern .",
			expectedOutput: "rg pattern",
			expectedOk:     true,
		},
		{
			name:           "RootSlash",
			input:          "grep -rnI pattern ./",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "CaseInsensitive",
			input:          "grep -rnIi pattern .",
			expectedOutput: "rg -n -i pattern",
			expectedOk:     true,
		},
		{
			name:           "WholeWord",
			input:          "grep -rnIiw pattern .",
			expectedOutput: "rg -n -i -w pattern",
			expectedOk:     true,
		},
		{
			name:           "FixedString",
			input:          "grep -rnIF pattern .",
			expectedOutput: "rg -n -F pattern",
			expectedOk:     true,
		},
		{
			name:           "FilesWithMatches",
			input:          "grep -rnIl pattern .",
			expectedOutput: "rg -n -l pattern",
			expectedOk:     true,
		},
		{
			name:           "Count",
			input:          "grep -rnIc pattern .",
			expectedOutput: "rg -n -c pattern",
			expectedOk:     true,
		},
		{
			name:           "InvertMatch",
			input:          "grep -rnIv pattern .",
			expectedOutput: "rg -n -v pattern",
			expectedOk:     true,
		},
		{
			name:           "OnlyMatching",
			input:          "grep -rnIo pattern .",
			expectedOutput: "rg -n -o pattern",
			expectedOk:     true,
		},
		{
			name:           "MaxCount",
			input:          "grep -rnI -m 5 pattern .",
			expectedOutput: "rg -n -m 5 pattern",
			expectedOk:     true,
		},
		{
			name:           "ContextC",
			input:          "grep -rnI -C 3 pattern .",
			expectedOutput: "rg -n -C 3 pattern",
			expectedOk:     true,
		},
		{
			name:           "ContextBA",
			input:          "grep -rnI -B 2 -A 2 pattern .",
			expectedOutput: "rg -n -B 2 -A 2 pattern",
			expectedOk:     true,
		},
		{
			name:           "WholeLine",
			input:          "grep -rnI -x pattern .",
			expectedOutput: "rg -n -x pattern",
			expectedOk:     true,
		},
		{
			name:           "IncludeGlob",
			input:          "grep -rnI --include=*.go pattern .",
			expectedOutput: "rg -n -g *.go pattern",
			expectedOk:     true,
		},
		{
			name:           "MultipleIncludes",
			input:          "grep -rnI --include=*.go --include=*.md pattern .",
			expectedOutput: "rg -n -g *.go -g *.md pattern",
			expectedOk:     true,
		},
		{
			name:           "ExcludeDir",
			input:          "grep -rnI --exclude-dir=.git --exclude-dir=target pattern .",
			expectedOutput: "rg -n -g !.git/** -g !target/** pattern",
			expectedOk:     true,
		},
		{
			name:           "ExcludeDirVendor",
			input:          "grep -rnI --exclude-dir=vendor pattern .",
			expectedOutput: "rg -n -g !vendor/** pattern",
			expectedOk:     true,
		},
		{
			name:           "ExcludeGlob",
			input:          "grep -rnI --exclude=*_test.go pattern .",
			expectedOutput: "rg -n -g !*_test.go pattern",
			expectedOk:     true,
		},
		{
			name:           "SymlinkFollow",
			input:          "grep -RnI pattern .",
			expectedOutput: "rg -L -n pattern",
			expectedOk:     true,
		},
		{
			name:           "DropH",
			input:          "grep -rnI -H pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "DropColor",
			input:          "grep -rnI --color=always pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "DropLineBuffered",
			input:          "grep -rnI --line-buffered pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "DropBinaryFiles",
			input:          "grep -rnI --binary-files=without-match pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "PreserveStderr",
			input:          "grep -rnI pattern . 2>/dev/null",
			expectedOutput: "rg -n pattern 2>/dev/null",
			expectedOk:     true,
		},
		{
			name:           "DropP",
			input:          "grep -rnI -P pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "DropG",
			input:          "grep -rnI -G pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "DropE",
			input:          "grep -rnI -E pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "DropZ",
			input:          "grep -rnI -z pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},
		{
			name:           "QuietMode",
			input:          "grep -rnI -q pattern .",
			expectedOutput: "rg -n -q pattern",
			expectedOk:     true,
		},
		{
			name:           "DropStarInclude",
			input:          "grep -rnI --include=* pattern .",
			expectedOutput: "rg -n pattern",
			expectedOk:     true,
		},

		// Negative cases: not a broad recursive grep from root
		{
			name:           "AlreadyRg",
			input:          "rg -n pattern",
			expectedOutput: "",
			expectedOk:     false,
		},
		{
			name:           "ExplicitFile",
			input:          "grep pattern file.go",
			expectedOutput: "",
			expectedOk:     false,
		},
		{
			name:           "SubdirTarget",
			input:          "grep -rn pattern src/",
			expectedOutput: "",
			expectedOk:     false,
		},
		{
			name:           "CdBeforeGrep",
			input:          "cd src && grep -rnI pattern .",
			expectedOutput: "",
			expectedOk:     false,
		},
		{
			name:           "NonRecursive",
			input:          "grep pattern .",
			expectedOutput: "",
			expectedOk:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output, ok := translateGrepToRg(tc.input)
			if output != tc.expectedOutput {
				t.Errorf("output mismatch: got %q, want %q", output, tc.expectedOutput)
			}
			if ok != tc.expectedOk {
				t.Errorf("ok mismatch: got %v, want %v", ok, tc.expectedOk)
			}
		})
	}
}
