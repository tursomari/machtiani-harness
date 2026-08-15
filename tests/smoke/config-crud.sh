#!/usr/bin/env bash
set -euo pipefail

CONFIG_PATH="$HOME/.machtiani/config.toml"
SCRATCH_DIR=$(mktemp -d)

cleanup_config_crud() {
  rm -rf "$SCRATCH_DIR"
}
trap cleanup_config_crud EXIT

fail() {
  echo "CONFIG SMOKE FAILURE: $*" >&2
  exit 1
}

assert_file_contains() {
  local expected=$1
  local file=$2
  grep -Fq -- "$expected" "$file" || fail "$file does not contain: $expected"
}

assert_file_not_contains() {
  local unexpected=$1
  local file=$2
  if grep -Fq -- "$unexpected" "$file"; then
    fail "$file unexpectedly contains: $unexpected"
  fi
}

expect_exit() {
  local expected=$1
  shift
  local stdout_file="$SCRATCH_DIR/expected-stdout"
  local stderr_file="$SCRATCH_DIR/expected-stderr"
  local actual

  set +e
  "$@" >"$stdout_file" 2>"$stderr_file"
  actual=$?
  set -e

  if [[ "$actual" -ne "$expected" ]]; then
    echo "Expected exit $expected, got $actual from: $*" >&2
    sed -n '1,120p' "$stdout_file" >&2
    sed -n '1,120p' "$stderr_file" >&2
    exit 1
  fi
}

checksum() {
  sha256sum "$1" | cut -d ' ' -f 1
}

assert_unchanged() {
  local expected=$1
  local actual
  actual=$(checksum "$CONFIG_PATH")
  [[ "$actual" == "$expected" ]] || fail "rejected mutation changed $CONFIG_PATH"
}

echo "==> Exercising the provider catalogue..."
machtiani config catalog list >"$SCRATCH_DIR/catalog-list"
assert_file_contains 'deepseek' "$SCRATCH_DIR/catalog-list"
assert_file_contains 'openai' "$SCRATCH_DIR/catalog-list"
machtiani config catalog show deepseek >"$SCRATCH_DIR/catalog-show"
assert_file_contains 'DEEPSEEK_API_KEY' "$SCRATCH_DIR/catalog-show"
assert_file_contains 'deepseek-v4-flash' "$SCRATCH_DIR/catalog-show"
machtiani config catalog show openrouter >"$SCRATCH_DIR/openrouter-catalog-show"
assert_file_contains 'https://openrouter.ai/api/v1/models' "$SCRATCH_DIR/openrouter-catalog-show"

echo "==> Verifying model search persists for an existing catalogue provider..."
searchable_config="$SCRATCH_DIR/searchable/config.toml"
machtiani config add --path "$searchable_config" \
  --preset openrouter \
  --api-key smoke-placeholder-key \
  --model '~openai/gpt-latest' \
  --alias openrouter-first \
  --no-interactive
searchable_checksum=$(checksum "$searchable_config")
export SEARCHABLE_CONFIG="$searchable_config"
expect >"$SCRATCH_DIR/searchable-provider-wizard" 2>&1 <<'EXPECT_EOF'
set timeout 10
spawn -noecho machtiani config add --path $env(SEARCHABLE_CONFIG)
expect {
  -re {Existing: openrouter} {}
  timeout { exit 10 }
  eof { exit 11 }
}
send "\r"
expect {
  -re {Search current model catalogue} {}
  timeout { exit 12 }
  eof { exit 13 }
}
send "\003"
expect eof
set child_status [lindex [wait] 3]
if {$child_status != 1} {
  exit 14
}
EXPECT_EOF
unset SEARCHABLE_CONFIG
assert_file_contains 'Existing: openrouter' "$SCRATCH_DIR/searchable-provider-wizard"
assert_file_contains 'Search current model catalogue' "$SCRATCH_DIR/searchable-provider-wizard"
assert_file_contains '→ Search current model catalogue' "$SCRATCH_DIR/searchable-provider-wizard"
[[ "$(checksum "$searchable_config")" == "$searchable_checksum" ]] || fail "cancelled searchable-provider wizard changed $searchable_config"

echo "==> Exercising preset config add and initial validation..."
machtiani config add \
  --preset deepseek \
  --url "$TEST_BASE_URL" \
  --api-key-env TEST_API_KEY \
  --model "$TEST_MODEL" \
  --alias deepseek-primary \
  --no-cache \
  --no-interactive

test -f "$CONFIG_PATH"
[[ "$(stat -c '%a' "$CONFIG_PATH")" == "600" ]] || fail "config permissions are not 0600"

echo "==> Initializing UUID-backed project state..."
machtiani init --no-interactive --config-scope global
machtiani project show --json >"$SCRATCH_DIR/project-show"
assert_file_contains '"status": "initialized"' "$SCRATCH_DIR/project-show"
assert_file_contains '"config_scope": "global"' "$SCRATCH_DIR/project-show"
assert_file_contains 'api_key = "${TEST_API_KEY}"' "$CONFIG_PATH"
assert_file_contains 'default_model = "deepseek-primary"' "$CONFIG_PATH"
assert_file_contains 'cache_enabled = false' "$CONFIG_PATH"
assert_file_contains 'context_length = 128000' "$CONFIG_PATH"
assert_file_contains 'endpoint = "/chat/completions"' "$CONFIG_PATH"
machtiani config check

echo "==> Verifying removed context controls fail with migration guidance..."
legacy_config="$SCRATCH_DIR/legacy-context.toml"
cp "$CONFIG_PATH" "$legacy_config"
sed -i '/\[planner\]/a max_input_tokens = 180000' "$legacy_config"
expect_exit 1 machtiani config check --path "$legacy_config"
assert_file_contains 'configure models.<alias>.context_length instead' "$SCRATCH_DIR/expected-stderr"
expect_exit 2 machtiani run --max-input-tokens 4096 -p ignored
assert_file_contains 'use --context-length' "$SCRATCH_DIR/expected-stderr"

echo "==> Verifying Manage models adds through the provider-first wizard..."
manager_checksum=$(checksum "$CONFIG_PATH")
expect >"$SCRATCH_DIR/manager-add-model-wizard" 2>&1 <<'EXPECT_EOF'
set timeout 10
spawn -noecho machtiani config
expect {
  -re {Choose an action} {}
  timeout { exit 20 }
  eof { exit 21 }
}
# Configuration: Finish, Add provider or model, Manage providers, Manage models.
send "\033\[B\033\[B\033\[B\r"
expect {
  -re {Models} {}
  timeout { exit 22 }
  eof { exit 23 }
}
# Models: List, Show, Add.
send "\033\[B\033\[B\r"
expect {
  -re {Choose a configured provider, a catalogue preset, or Other} {}
  timeout { exit 24 }
  eof { exit 25 }
}
expect {
  -re {Existing: deepseek} {}
  timeout { exit 26 }
  eof { exit 27 }
}
# Cancel the nested add flow, model menu, and configuration manager.
send "\003"
after 200
send "\003"
after 200
send "\003"
expect eof
EXPECT_EOF
assert_file_contains 'Existing: deepseek' "$SCRATCH_DIR/manager-add-model-wizard"
[[ "$(checksum "$CONFIG_PATH")" == "$manager_checksum" ]] || fail "cancelled manager add-model wizard changed $CONFIG_PATH"

echo "==> Verifying re-init is idempotent and protects existing state..."
runtime_sentinels=(
  ".machtiani/sessions/init-safety-session/chat/agent-final-answer.md"
  ".machtiani/artifacts/init-safety-artifact.txt"
  ".machtiani/modes/init-safety-mode/instruction.md"
  ".machtiani/templates/init-safety-template.txt"
  ".machtiani/tmp/init-safety-state/session.lock"
)
for sentinel in "${runtime_sentinels[@]}"; do
  mkdir -p "$(dirname "$sentinel")"
  printf 'preserve-existing-machtiani-data\n' >"$sentinel"
done
initial_checksum=$(checksum "$CONFIG_PATH")
expect_exit 0 machtiani init --no-interactive
assert_file_contains 'Project initialized:' "$SCRATCH_DIR/expected-stdout"
assert_unchanged "$initial_checksum"
for sentinel in "${runtime_sentinels[@]}"; do
  test -f "$sentinel" || fail "machtiani init removed existing runtime file $sentinel"
  assert_file_contains 'preserve-existing-machtiani-data' "$sentinel"
done

echo "==> Exercising provider list, show, add, set, rename, and remove..."
machtiani config provider list >"$SCRATCH_DIR/provider-list"
assert_file_contains 'deepseek' "$SCRATCH_DIR/provider-list"
machtiani config provider show deepseek >"$SCRATCH_DIR/provider-show"
assert_file_contains '${TEST_API_KEY}' "$SCRATCH_DIR/provider-show"

machtiani config provider set deepseek \
  --url "${TEST_BASE_URL%/}/" \
  --endpoint /chat/completions \
  --header X-Smoke=enabled \
  --query smoke=true \
  --no-interactive
assert_file_contains 'endpoint = "/chat/completions"' "$CONFIG_PATH"
assert_file_contains 'X-Smoke = "enabled"' "$CONFIG_PATH"
assert_file_contains 'smoke = "true"' "$CONFIG_PATH"

machtiani config provider set deepseek \
  --url "$TEST_BASE_URL" \
  --api-key-env TEST_API_KEY \
  --clear-endpoint \
  --remove-header X-Smoke \
  --remove-query smoke \
  --no-interactive
assert_file_not_contains 'X-Smoke' "$CONFIG_PATH"
assert_file_not_contains 'smoke = "true"' "$CONFIG_PATH"
assert_file_not_contains 'endpoint = ' "$CONFIG_PATH"

machtiani config provider rename deepseek deepseek-renamed --no-interactive
machtiani config check
assert_file_contains 'provider = "deepseek-renamed"' "$CONFIG_PATH"
machtiani config provider rename deepseek-renamed deepseek --no-interactive

machtiani config provider add scratch-provider \
  --url https://invalid.example/v1 \
  --api-key smoke-placeholder-key \
  --endpoint /chat/completions \
  --header X-Scratch=yes \
  --query test=true \
	--reasoning-format reasoning \
  --no-interactive
machtiani config provider show scratch-provider >"$SCRATCH_DIR/scratch-provider-show"
assert_file_contains '[redacted]' "$SCRATCH_DIR/scratch-provider-show"
assert_file_contains 'reasoning_format: reasoning' "$SCRATCH_DIR/scratch-provider-show"
machtiani config provider set scratch-provider --clear-api-key --clear-endpoint \
  --remove-header X-Scratch --remove-query test --reasoning-format auto --no-interactive
machtiani config provider set scratch-provider --api-key-env TEST_API_KEY --no-interactive
machtiani config provider remove scratch-provider --no-interactive

before=$(checksum "$CONFIG_PATH")
expect_exit 1 machtiani config provider remove deepseek --no-interactive
assert_unchanged "$before"

echo "==> Exercising model list, show, add, set, rename, default, and remove..."
machtiani config model list >"$SCRATCH_DIR/model-list"
assert_file_contains 'deepseek-primary' "$SCRATCH_DIR/model-list"
machtiani config model show deepseek-primary >"$SCRATCH_DIR/model-show"
assert_file_contains "model: $TEST_MODEL" "$SCRATCH_DIR/model-show"
assert_file_contains 'context_length: 128000 (inherited)' "$SCRATCH_DIR/model-show"

machtiani config model add deepseek-scratch \
  --provider deepseek \
  --model "$TEST_MODEL" \
  --context-length 64000 \
  --reasoning low \
  --param smoke=one \
  --param-json '{"temperature":0.1,"reasoning":{"effort":"low","budget_tokens":null,"enabled":true}}' \
  --no-interactive
assert_file_contains 'params_json = ' "$CONFIG_PATH"
assert_file_contains 'budget_tokens' "$CONFIG_PATH"
assert_file_contains 'context_length = 64000' "$CONFIG_PATH"
machtiani config model set deepseek-scratch --clear-context-length --no-interactive
machtiani config model show deepseek-scratch >"$SCRATCH_DIR/model-context-inherited"
assert_file_contains 'context_length: 128000 (inherited)' "$SCRATCH_DIR/model-context-inherited"

for reasoning in medium high xhigh max provider-special; do
  machtiani config model set deepseek-scratch --reasoning "$reasoning" --no-interactive
  assert_file_contains "reasoning_effort = \"$reasoning\"" "$CONFIG_PATH"
done

before=$(checksum "$CONFIG_PATH")
expect_exit 2 machtiani config model set deepseek-scratch --reasoning xhih --no-interactive
assert_unchanged "$before"

machtiani config model set deepseek-scratch \
  --provider deepseek \
  --model "$TEST_MODEL" \
  --clear-reasoning \
	--clear-params-json \
  --remove-param smoke \
  --remove-param temperature \
  --remove-param enabled \
  --no-interactive
assert_file_not_contains 'provider-special' "$CONFIG_PATH"
assert_file_not_contains 'temperature = ' "$CONFIG_PATH"

machtiani config model rename deepseek-scratch deepseek-review --no-interactive
machtiani config model default deepseek-review --no-interactive
assert_file_contains 'default_model = "deepseek-review"' "$CONFIG_PATH"

before=$(checksum "$CONFIG_PATH")
expect_exit 2 machtiani config model remove deepseek-review --no-interactive
assert_unchanged "$before"

machtiani config model remove deepseek-review \
  --replacement deepseek-primary \
  --no-interactive
assert_file_contains 'default_model = "deepseek-primary"' "$CONFIG_PATH"
assert_file_not_contains 'deepseek-review' "$CONFIG_PATH"

before=$(checksum "$CONFIG_PATH")
expect_exit 1 machtiani config model add deepseek-primary \
  --provider deepseek --model "$TEST_MODEL" --no-interactive
assert_unchanged "$before"
expect_exit 1 machtiani config model set deepseek-primary \
  --provider missing-provider --no-interactive
assert_unchanged "$before"

echo "==> Exercising global and per-model cache commands..."
machtiani config cache show >"$SCRATCH_DIR/cache-global-initial"
machtiani config cache enable --no-interactive
machtiani config cache set \
  --key-name cache_control \
  --control-json '{"type":"ephemeral"}' \
  --trigger-threshold 8192 \
  --lookback-offset 2 \
  --reanchor-tokens 4096 \
  --reanchor-messages 20 \
  --min-cached-tokens 2048 \
  --no-interactive
assert_file_contains 'cache_trigger_threshold = 8192' "$CONFIG_PATH"
assert_file_contains 'cache_reanchor_messages = 20' "$CONFIG_PATH"

machtiani config cache enable --model deepseek-primary --no-interactive
machtiani config cache set --model deepseek-primary \
  --trigger-threshold 12288 \
  --lookback-offset 3 \
  --no-interactive
machtiani config cache show --model deepseek-primary >"$SCRATCH_DIR/cache-model-override"
assert_file_contains '(model override)' "$SCRATCH_DIR/cache-model-override"
machtiani config cache disable --model deepseek-primary --no-interactive
machtiani config cache inherit --model deepseek-primary --no-interactive
machtiani config cache show --model deepseek-primary >"$SCRATCH_DIR/cache-model-inherited"
assert_file_contains '(inherited)' "$SCRATCH_DIR/cache-model-inherited"
machtiani config cache disable --no-interactive

before=$(checksum "$CONFIG_PATH")
expect_exit 2 machtiani config cache set --trigger-threshold -1 --no-interactive
assert_unchanged "$before"

echo "==> Exercising config show, check, and failure safety..."
machtiani config show >"$SCRATCH_DIR/config-show"
machtiani config show --full >"$SCRATCH_DIR/config-show-full"
machtiani config show --key models.deepseek-primary >"$SCRATCH_DIR/config-show-model"
assert_file_contains '********' "$SCRATCH_DIR/config-show-full"
machtiani config check

before=$(checksum "$CONFIG_PATH")
expect_exit 2 machtiani config add --provider incomplete --no-interactive
assert_unchanged "$before"
expect_exit 1 machtiani config check --global --path "$SCRATCH_DIR/conflict.toml"
assert_unchanged "$before"
expect_exit 1 machtiani config
assert_unchanged "$before"

echo "==> Exercising --path, --global, and MACHTIANI_CONFIG selection..."
path_config="$SCRATCH_DIR/path/config.toml"
env_config="$SCRATCH_DIR/environment/config.toml"
override_config="$SCRATCH_DIR/override/config.toml"
global_home="$SCRATCH_DIR/home"

machtiani config add --path "$path_config" \
  --provider path-provider --url https://invalid.example/v1 \
  --api-key-env TEST_API_KEY --model path-model --alias path-model \
  --no-interactive
machtiani config check --path "$path_config" >"$SCRATCH_DIR/path-check" 2>&1
assert_file_contains "Config file: $path_config" "$SCRATCH_DIR/path-check"

MACHTIANI_CONFIG="$env_config" machtiani config add \
  --provider env-provider --url https://invalid.example/v1 \
  --api-key-env TEST_API_KEY --model env-model --alias env-model \
  --no-interactive
MACHTIANI_CONFIG="$env_config" machtiani config check >"$SCRATCH_DIR/env-check" 2>&1
assert_file_contains "Config file: $env_config" "$SCRATCH_DIR/env-check"

MACHTIANI_CONFIG="$env_config" machtiani config add --path "$override_config" \
  --provider override-provider --url https://invalid.example/v1 \
  --api-key-env TEST_API_KEY --model override-model --alias override-model \
  --no-interactive >"$SCRATCH_DIR/override-add" 2>&1
assert_file_contains 'ignoring MACHTIANI_CONFIG=' "$SCRATCH_DIR/override-add"
assert_file_contains "Config file: $override_config" "$SCRATCH_DIR/override-add"

HOME="$global_home" machtiani config add --global \
  --provider global-provider --url https://invalid.example/v1 \
  --api-key-env TEST_API_KEY --model global-model --alias global-model \
  --no-interactive
test -f "$global_home/.machtiani/config.toml"
HOME="$global_home" machtiani config check --global

echo "==> Verifying final DeepSeek configuration..."
machtiani config check
machtiani config provider list
machtiani config provider show deepseek
machtiani config model list
machtiani config model show deepseek-primary
machtiani config cache show

assert_file_contains 'default_model = "deepseek-primary"' "$CONFIG_PATH"
assert_file_contains 'api_key = "${TEST_API_KEY}"' "$CONFIG_PATH"
assert_file_contains 'cache_enabled = false' "$CONFIG_PATH"
assert_file_not_contains 'scratch-provider' "$CONFIG_PATH"
assert_file_not_contains 'deepseek-review' "$CONFIG_PATH"
assert_file_not_contains 'X-Smoke' "$CONFIG_PATH"
assert_file_not_contains 'endpoint = ' "$CONFIG_PATH"

echo "CONFIG CRUD SMOKE PASSED"
