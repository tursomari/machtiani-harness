package cli

import (
	"fmt"
)

func printHelp() {
	helpText := `Usage: mct <command> [flags]

Machtiani (mct) — code chat for large, real codebases.

Commands:
  prompt        Run a chat/prompt against this repository.
  help          Show this help message.

Prompt:
  mct prompt "..." [flags]
  mct prompt --file path.md [flags]

  Flags:
    -f, --file <path>        Markdown file used as the prompt. Required if no positional message is provided.
        --model <string>     Model alias defined in .machtiani/config.toml.
        --openai-model <str> Direct upstream model name (deprecated; prefer --model).
        --openai-api-key     OpenAI-compatible API key (overrides env OPENAI_API_KEY).
        --openai-base-url    OpenAI-compatible base URL (overrides env OPENAI_BASE_URL).
        --agent-model <str>  Agent model for applying patches (defaults to --model).
        --session <string>   Session identifier used to scope conversation history.
        --match-strength     Context match strength: high | mid | low. Default: mid.
        --mode <string>      Mode: chat | pure-chat | answer-only | default. Default: default.
        --max-input-tokens   Maximum number of tokens allowed in the constructed prompt (0 disables truncation).
        --verbose            Print verbose/log output.

Examples:
  Prompt chat with explicit message:
    mct prompt "Refactor payment module." --model anthropic/claude-3.7-sonnet:thinking --mode chat

  Prompt chat from a markdown file:
    mct prompt --file .machtiani/sessions/session-123/chat/my_chat.md --model deepseek-coder

  Specify stricter context match:
    mct prompt "Summarize architecture and main APIs." --model Qwen2.5-Coder-1.5B-Instruct --match-strength high

More info:
  - File ignores: list paths in .machtiani.ignore to exclude from retrieval.

Machtiani - code chat for real projects, thousands of files and commits.`
	fmt.Println(helpText)
}
