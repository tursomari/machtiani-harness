package cli

import (
	"fmt"
)

func printHelp() {
	helpText := `Usage: mct <command> [flags]

Machtiani (mct) — code chat for large, real codebases.

Commands:
  prompt        Run a chat/prompt against this repository.
  sync          Add or sync a project repository with machtiani.
  remove        Remove a repository from the machtiani system.
  status        View the indexing/status of this repository.
  help          Show this help message.

Prompt:
  mct prompt "..." [flags]
  mct prompt --file path.md [flags]

  Flags:
    -f, --file <path>        Markdown file used as the prompt. Required if no positional message is provided.
        --model <string>     LLM model name (e.g., gpt-4o-mini).
        --agent-model <str>  Agent model for applying patches (defaults to --model).
        --no-codex           Disable agent file retrieval (no-codex mode).
        --match-strength      Context match strength: high | mid | low. Default: mid
        --mode <string>       Mode: chat | pure-chat | answer-only | default. Default: default
        --force               Skip confirmation for file changes.
        --verbose             Print verbose/log output.
        --remote <name>       Git remote name. Default: origin

Sync:
  mct sync [flags]
    --model <string>       Specify LLM model.
    --model-threads <n>    Number of sync LLM requests in parallel (default: 0 = auto)
    --amplify <level>      Data amplification: off | low | mid | high. Default: off
    --depth <n>            Number of most recent commits to sync (default: 10000)
    --force                Skip sync confirmation prompt
    --cost                 Estimate LLM/token cost before performing sync
    --cost-only            Estimate token usage and exit without syncing
    --remote <name>        Git remote name (default: origin)

Remove:
  mct remove [flags]
    --force                Skip confirmation prompt
    --remote <name>        Git remote name (default: origin)

Examples:
  See if a project is ready to chat:
    mct status

  Prompt chat with explicit message:
    mct prompt "Refactor payment module." --model anthropic/claude-3.7-sonnet:thinking --mode chat

  Prompt chat from a markdown file:
    mct prompt --file .machtiani/chat/my_chat.md --model deepseek-coder

  Specify stricter context match:
    mct prompt "Summarize architecture and main APIs." --model Qwen2.5-Coder-1.5B-Instruct --match-strength high

  Add/sync project with high concurrency:
    mct sync --amplify low --model google/gemini-2.0-flash-001 --model-threads 10 --force

  Only estimate sync token/cost, do not sync:
    mct sync --cost-only --model gpt-4o-mini

  Remove a project from machtiani, without confirmation:
    mct remove --force

More info:
  - File ignores: List paths in .machtiani.ignore to exclude from retrieval/sync.
  - Sync/project status:      mct status

Machtiani - code chat for real projects, thousands of files and commits.
`
	fmt.Println(helpText)
}
