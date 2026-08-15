# Machtiani Configuration Guide

`machtiani config` creates and manages the unified Machtiani configuration used
by `machtiani`, `mct`, and `shell-agent`. The configuration connects local model
aliases to named providers and stores shared defaults such as prompt caching.

For the complete TOML schema beyond providers and models, see
[`examples/config.comprehensive.toml`](examples/config.comprehensive.toml).

## Interactive and scripted operation

Use `init` from a project for first-time setup:

```bash
machtiani init
```

`init` creates `.machtiani/project.uuid`, the private
`~/.machtiani/<uuid>/` project store, and the canonical mode library under
`~/.machtiani/modes/`. It explains that global configuration shares providers
and models while sessions remain project-specific, then asks
`Use global config? [Y/n] (recommended)` on a terminal. Re-running it keeps the
existing UUID and config.

Every setup choice has a script-safe form. This selects global scope without
reading stdin:

```bash
machtiani init --no-interactive --config-scope global
```

Use `--config-scope project` to select a complete UUID-scoped config. Provider
creation flags accepted by `config add`—including `--preset`, `--provider`,
`--url`, `--model`, and `--api-key-env`—can be passed to init when the selected
file does not yet exist.

Run the configuration manager without a subcommand for follow-up changes:

```bash
machtiani config
```

If the selected file does not exist, this opens initial setup. Otherwise it
opens menus for providers, models, the default model, caching, and validation.
Choosing **Manage models → Add model** uses the same provider-first setup flow:
select an existing provider, a catalogue preset, or `Other provider` before
choosing or entering the model.

Interactive menus refresh the visible terminal after each selection. A submenu
replaces its parent, and an action result is shown above only the current menu;
the refresh does not erase existing terminal scrollback.

Menus honor `[ui].theme` and `[ui].glyphs` from the selected configuration and
the `MACHTIANI_THEME` and `MACHTIANI_GLYPHS` overrides. The current selection is emphasized using the
theme's semantic selection color. **Finish** has a distinct success color while
unselected and ordinary selection styling while selected. `NO_COLOR`,
`theme = "none"`, dumb terminals, and non-terminal output retain readable plain
text markers without color. Glyph mode remains independent: `unicode` is the
default, while `ascii` avoids box-drawing and symbol glyphs.

Mutating commands are interactive by default. Supplied flags prefill answers;
the command prompts for missing information and confirms before writing.

Use `--no-interactive` for scripts and CI:

```bash
export OPENAI_API_KEY=sk-...
machtiani config add --preset openai --no-interactive
```

`--no-interactive` is the only prompt-bypass flag. It guarantees that the
command never reads stdin, skips confirmation, and reports missing arguments as
an error. A mutating command attached to a non-terminal must include it.

## Selecting the configuration file

All config commands accept these target flags:

| Flag | Target |
| --- | --- |
| `--path <file>` | Exactly the supplied path |
| `--global` | `$HOME/.machtiani/config.toml` |
| `--project` | `$HOME/.machtiani/<uuid>/config.toml` for the initialized project |

The three flags are mutually exclusive. Without one, selection uses this order:

1. `MACHTIANI_CONFIG`, when set.
2. The UUID-project config when its stored scope is `project`.
3. A repo-local `.machtiani/config.toml` for an unmigrated legacy project.
4. `$HOME/.machtiani/config.toml`.

Every command displays the selected absolute path. An explicit `--path` or
An explicit target overrides `MACHTIANI_CONFIG` and reports that the
environment value was ignored.

Examples:

```bash
machtiani config --global
machtiani config check --project
machtiani config check --path ./configs/agent.toml
machtiani config model list --global
```

The selected project scope is itself scriptable:

```bash
machtiani config scope show --json
machtiani config scope use project --copy-global --no-interactive
machtiani config scope use global --no-interactive
```

## Adding a provider and model together

`config add` is the shortest path for initial setup and for adding another
model/provider set:

```text
machtiani config add [flags]
```

| Flag | Meaning |
| --- | --- |
| `--preset <id>` | Fill values from the built-in provider catalogue |
| `--provider <name>` | Local provider name |
| `--url <url>` | Base URL for a new provider |
| `--endpoint <path>` | Optional request endpoint for a new provider |
| `--api-key <value>` | Literal API key for a new provider |
| `--api-key-env <name>` | Store `${NAME}` instead of a literal key |
| `--header <key=value>` | Provider header; repeatable |
| `--query <key=value>` | Provider query parameter; repeatable |
| `--model <id>` | Exact upstream model identifier |
| `--alias <name>` | Local model alias |
| `--reasoning <value>` | Optional reasoning effort |
| `--default` | Make the added model the default |
| `--no-cache` | Disable global caching when creating a new file |
| `--no-interactive` | Require complete arguments and never prompt |

When `--provider` names an existing provider, its URL and credentials are
reused. Provider-definition flags are rejected in that case; use `provider set`
to modify the existing provider. A new provider requires a URL and either
`--api-key` or `--api-key-env`.

The first model becomes the default automatically. Interactive additions ask
whether a later model should become the default and offer to add another set.
`--no-cache` is only valid while creating a new configuration; later additions
do not change the existing global cache policy.

## Built-in provider catalogue

The embedded catalogue supplies setup defaults for OpenAI-compatible providers
that Machtiani can configure directly:

```text
machtiani config catalog list
machtiani config catalog show <provider>
machtiani config catalog show <provider> --json
```

The initial catalogue contains `deepseek`, `openai`, and `openrouter`. Each
preset defines its base URL, chat endpoint, conventional API-key environment
variable, default model and local alias, known reasoning choices, and model
cache compatibility. The values are intentionally scoped instead of mirroring
every entry in a general provider database: providers with a native request
protocol are not offered until the runtime supports that protocol.

Catalogue providers may also define a live model-discovery URL. OpenRouter does
so because its available models change frequently. Its interactive model menu
contains `Search current model catalogue`; after the user enters part of a name
or model ID, the wizard retrieves the current list with bearer authentication
and displays up to 25 matching IDs. Discovery happens only when that option is
selected. A request failure falls back to manual model entry, and `Other model`
is always available without discovery. Existing providers are matched back to
their catalogue entry by provider ID or base URL, so search remains available
when adding another model later or continuing the same setup session.

Catalogue search is initially selected whenever a provider supports searchable
models, so pressing Enter on OpenRouter starts a search rather than selecting
the stable flagship alias. After choosing a model, the alias prompt shows the
suggested shortened name and explicitly states that Enter accepts it.

Live discovery does not change scripted behavior. `--preset openrouter
--no-interactive` uses the catalogue's stable default, while `--model <id>`
selects any exact OpenRouter model deterministically. `config catalog show
openrouter` displays the discovery endpoint for inspection.

For example, this creates a complete DeepSeek configuration containing an
environment reference, without putting the live key in TOML or on the command
line:

```bash
export DEEPSEEK_API_KEY=...
machtiani config add --preset deepseek --no-interactive
```

Preset values are starting points. Explicit flags override the URL, endpoint,
credential reference, model, alias, reasoning, headers, or query parameters:

```bash
machtiani config add \
  --preset openrouter \
  --model vendor/new-model \
  --alias experimental \
  --reasoning provider-specific \
  --header X-Tenant=team-a \
  --no-interactive
```

Interactive menus always include `Other provider`, `Other model`, and `Other`
reasoning choices. Arbitrary reasoning values pass through after typo checks
for common `xhigh` and `max` misspellings. For a completely custom integration,
omit `--preset` and supply `--provider`, `--url`, credentials, `--model`, and
`--alias`; `--endpoint`, `--header`, and `--query` expose the remaining provider
transport settings.

The catalogue is consulted only while adding a provider/model set. Generated
TOML remains explicit, so a future catalogue update never silently changes an
existing configuration. Catalogue source values are maintained against the
[DeepSeek API documentation](https://api-docs.deepseek.com/),
[OpenAI model documentation](https://developers.openai.com/api/docs/models),
and [OpenRouter quickstart](https://openrouter.ai/docs/quickstart).

Prefer `--api-key-env` in scripts so secrets do not enter shell history or the
configuration file:

```bash
export OPENAI_API_KEY=sk-...
machtiani config add \
  --provider openai \
  --url https://api.openai.com/v1 \
  --api-key-env OPENAI_API_KEY \
  --model gpt-5 \
  --alias primary \
  --no-interactive
```

This writes:

```toml
[providers.openai]
base_url = "https://api.openai.com/v1"
api_key = "${OPENAI_API_KEY}"

[models.primary]
provider = "openai"
model = "gpt-5"
```

## Provider commands

```text
machtiani config provider list
machtiani config provider show <name>
machtiani config provider add [<name>] [flags]
machtiani config provider set [<name>] [flags]
machtiani config provider rename <old> <new> [flags]
machtiani config provider remove [<name>] [flags]
```

`list` prints provider names and base URLs. `show` displays the URL, endpoint,
headers, and query values. Literal API keys are always redacted; environment
references are displayed by name.

Provider `add` and `set` support:

| Flag | Meaning |
| --- | --- |
| `--url <url>` | Set `base_url` |
| `--api-key <value>` | Store a literal API key |
| `--api-key-env <name>` | Store an environment reference |
| `--clear-api-key` | Remove the configured API key |
| `--endpoint <path>` | Set a provider-specific endpoint |
| `--clear-endpoint` | Remove the endpoint override |
| `--reasoning-format <format>` | Set `auto`, `reasoning_effort`, `reasoning`, or `reasoning_explicit` |
| `--header <key=value>` | Add or replace a header; repeatable |
| `--remove-header <key>` | Remove a header; repeatable |
| `--query <key=value>` | Add or replace a query parameter; repeatable |
| `--remove-query <key>` | Remove a query parameter; repeatable |

Examples:

```bash
machtiani config provider add azure \
  --url https://example.openai.azure.com \
  --api-key-env AZURE_OPENAI_API_KEY \
  --endpoint /openai/deployments/coder/chat/completions \
  --query api-version=2025-04-01-preview

machtiani config provider set azure \
  --header X-Client=machtiani \
  --remove-query old-parameter \
  --reasoning-format reasoning_effort

machtiani config provider rename azure azure-production
```

Renaming a provider updates every model that references it. A provider cannot
be removed while models still reference it; reassign or remove those models
first. Provider removal never cascades.

## Model commands

```text
machtiani config model list
machtiani config model show <alias>
machtiani config model add [<alias>] [flags]
machtiani config model set [<alias>] [flags]
machtiani config model rename <old> <new> [flags]
machtiani config model remove [<alias>] [flags]
machtiani config model default [<alias>] [flags]
```

`list` displays each alias, provider, upstream model identifier, and marks the
default with `*`. `show` displays one model's configured definition.

Model `add` and `set` support:

| Flag | Meaning |
| --- | --- |
| `--provider <name>` | Assign the model to an existing provider |
| `--model <id>` | Set the upstream model identifier |
| `--reasoning <value>` | Set reasoning effort |
| `--clear-reasoning` | Remove the override and use the provider default |
| `--param <key=value>` | Add or replace a string request parameter; repeatable |
| `--param-json <json>` | Store an inline JSON object that overlays native `params` |
| `--clear-params-json` | Remove the inline JSON request parameters |
| `--remove-param <key>` | Remove a request parameter; repeatable |

Reasoning accepts `low`, `medium`, `high`, `xhigh`, `max`, and arbitrary
provider-specific values. Interactive operation checks likely misspellings of
`xhigh` and `max` and confirms unknown values. Noninteractive operation rejects
likely misspellings but otherwise passes provider-specific values through.

Examples:

```bash
machtiani config model add reviewer \
  --provider openai \
  --model gpt-5 \
  --reasoning xhigh

machtiani config model set reviewer \
  --param-json '{"reasoning":{"effort":"high","budget_tokens":null,"enabled":true},"max_tokens":8192}'

machtiani config model set reviewer --clear-reasoning
machtiani config model default reviewer
machtiani config model rename reviewer final-reviewer
```

### Reasoning request compatibility

OpenAI-compatible APIs use more than one wire format for reasoning effort.
Machtiani sends `reasoning_effort = "high"` by default. OpenRouter and
DeepInfra prefer `reasoning = { effort = "high" }`. The provider catalogue or
`providers.<name>.reasoning_format` can override that preference.

When a provider returns a reasoning-parameter-specific HTTP 400 before any
streamed output, the agent tries the compatible forms in this order (adjusted
so the provider preference is first):

1. `{"reasoning_effort":"high"}`
2. `{"reasoning":{"effort":"high"}}`
3. `{"reasoning":{"effort":"high","budget_tokens":null,"enabled":true}}`

The successful form is reused for that provider endpoint and model for the
rest of the process. A warning reports the switch; `config.toml` is never
rewritten automatically. Unrelated HTTP errors are not retried.

Use `params_json` for provider-specific structures that TOML cannot represent
exactly, especially JSON `null`. It remains a quoted inline JSON object in
`config.toml`, and its keys take precedence over `[models.<alias>.params]`:

```toml
[models.reviewer]
provider = "custom"
model = "review-model"
params_json = '''{"reasoning":{"effort":"high","budget_tokens":null,"enabled":true}}'''
```

In the wizard, use `Manage models` → `Additional request parameters` to view,
replace, or clear this JSON object.

Renaming a model updates `default_model`, `shell_agent_model`, `answer_model`,
`file_discovery_model`, and legacy `[model].model_name` references. Removing a
referenced model requires selecting a replacement interactively or supplying
`--replacement <alias>` with `--no-interactive`:

```bash
machtiani config model remove old-model \
  --replacement primary \
  --no-interactive
```

The only configured model cannot be removed because a valid default must
remain.

## Prompt-cache commands

```text
machtiani config cache show [--model <alias>]
machtiani config cache enable [--model <alias>]
machtiani config cache disable [--model <alias>]
machtiani config cache inherit --model <alias>
machtiani config cache set [--model <alias>] [flags]
```

Without `--model`, cache commands operate on `[model_defaults]`. With
`--model`, they operate on one `[models.<alias>]` override.

- Global `enable` sets `cache_enabled = true` and fills missing canonical cache
  settings.
- Global `disable` sets `cache_enabled = false` without deleting tuning values.
- Model `enable` or `disable` writes a model-specific boolean override.
- Model `inherit` removes every cache override from that model.
- `show --model` labels values as inherited or model-specific.

`cache set` accepts:

| Flag | TOML key |
| --- | --- |
| `--key-name <name>` | `cache_key_name` |
| `--control-json <json>` | `cache_control` |
| `--trigger-threshold <tokens>` | `cache_trigger_threshold` |
| `--lookback-offset <messages>` | `cache_lookback_offset` |
| `--reanchor-tokens <tokens>` | `cache_reanchor_tokens` |
| `--reanchor-messages <count>` | `cache_reanchor_messages` |
| `--min-cached-tokens <tokens>` | `cache_reanchor_min_cached_tokens` |

Numeric values must be non-negative.

Examples:

```bash
machtiani config cache enable --no-interactive
machtiani config cache disable --model uncached --no-interactive
machtiani config cache inherit --model uncached --no-interactive

machtiani config cache set \
  --trigger-threshold 8192 \
  --lookback-offset 2 \
  --control-json '{"type":"ephemeral"}' \
  --no-interactive
```

## Inspection and validation

Validate relationships and value types:

```bash
machtiani config check
```

Validation checks that providers and models are well formed, every model names
an existing provider, selectors name configured models, and cache thresholds
are non-negative.

Display the effective configuration with source annotations:

```bash
machtiani config show
machtiani config show --full
machtiani config show --key models.primary
```

API keys are redacted from inspection output.

## Write and error behavior

- `add` fails if the provider or model already exists.
- `set`, `rename`, and `remove` fail if the target does not exist.
- A proposed change is validated before replacing the original file.
- Writes use a temporary file and atomic rename with permissions `0600`.
- Unknown parsed TOML keys are retained.
- TOML is canonically re-encoded, so comments and original ordering may be lost.
- Rejected or cancelled changes leave the original file untouched.

Exit status `0` means success or a user-cancelled interactive confirmation,
`1` means a configuration or filesystem failure, and `2` means invalid command
usage or missing noninteractive arguments.

## Initial setup and compatibility

`machtiani init` initializes both project identity and configuration. The former
`--provider-url`, `--api-key`, `--model`, `--reasoning`, `--alias`, and `--force`
init flags have been removed so there is only one setup model. Automation can
use `machtiani init --no-interactive` with configuration flags, or manage an
already selected target with `machtiani config ... --no-interactive`.

Legacy repo-local state remains readable until explicitly migrated. Review and
execute migration without menus as follows:

```bash
machtiani migrate --dry-run --json
machtiani migrate --no-interactive --yes --json
```

The command copies through a private staging directory, checksum-verifies every
regular file, writes the UUID marker, and then archives migrated legacy entries.
Use `--keep-legacy` when the verified source copy must remain in place.

The former scalar commands `config url`, `config api-key`, `config model
<identifier>`, and `config reasoning` have been removed. Use the corresponding
`config provider set` or `config model set` resource command instead.
