package cli

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"
)

func printHelp() {
	fs := pflag.NewFlagSet("mct", pflag.ContinueOnError)
	_ = registerPromptFlags(fs)

	fmt.Fprintln(os.Stderr, "Usage: mct <command> [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Machtiani (mct) — code chat for large, real codebases.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  prompt        Run a chat/prompt against this repository.")
	fmt.Fprintln(os.Stderr, "  help          Show this help message.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Prompt:")
	fmt.Fprintln(os.Stderr, "  mct prompt \"...\" [flags]")
	fmt.Fprintln(os.Stderr, "  mct prompt --file path.md [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Options:")
	fs.SetOutput(os.Stderr)
	fs.PrintDefaults()
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Examples:")
	fmt.Fprintln(os.Stderr, "  Prompt chat with explicit message:")
	fmt.Fprintln(os.Stderr, "    mct prompt \"Refactor payment module.\" --model anthropic/claude-3.7-sonnet:thinking --mode chat")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Prompt chat from a markdown file:")
	fmt.Fprintln(os.Stderr, "    mct prompt --file .machtiani/sessions/session-123/chat/my_chat.md --model deepseek-coder")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Specify stricter context match:")
	fmt.Fprintln(os.Stderr, "    mct prompt \"Summarize architecture and main APIs.\" --model Qwen2.5-Coder-1.5B-Instruct --match-strength high")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "More info:")
	fmt.Fprintln(os.Stderr, "  - File ignores: list paths in .machtiani.ignore to exclude from retrieval.")
	fmt.Fprintln(os.Stderr, "  - Dynamic routing: a preflight LLM check routes between default retrieval and shell-agent execution automatically.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Machtiani - code chat for real projects, thousands of files and commits.")
}
