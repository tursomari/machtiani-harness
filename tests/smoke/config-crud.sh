#!/usr/bin/env bash
set -euo pipefail

CONFIG_PATH="$PWD/.machtiani/config.toml"
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

echo "==> Exercising config add and initial validation..."
mct-agent config add \
  --provider deepseek \
  --url "$TEST_BASE_URL" \
  --api-key-env TEST_API_KEY \
  --model "$TEST_MODEL" \
  --alias deepseek-primary \
  --no-cache \
  --no-interactive

test -f "$CONFIG_PATH"
[[ "$(stat -c '%a' "$CONFIG_PATH")" == "600" ]] || fail "config permissions are not 0600"
assert_file_contains 'api_key = "${TEST_API_KEY}"' "$CONFIG_PATH"
assert_file_contains 'default_model = "deepseek-primary"' "$CONFIG_PATH"
assert_file_contains 'cache_enabled = false' "$CONFIG_PATH"
mct-agent config check

echo "==> Exercising provider list, show, add, set, rename, and remove..."
mct-agent config provider list >"$SCRATCH_DIR/provider-list"
assert_file_contains 'deepseek' "$SCRATCH_DIR/provider-list"
mct-agent config provider show deepseek >"$SCRATCH_DIR/provider-show"
assert_file_contains '${TEST_API_KEY}' "$SCRATCH_DIR/provider-show"

mct-agent config provider set deepseek \
  --url "${TEST_BASE_URL%/}/" \
  --endpoint /chat/completions \
  --header X-Smoke=enabled \
  --query smoke=true \
  --no-interactive
assert_file_contains 'endpoint = "/chat/completions"' "$CONFIG_PATH"
assert_file_contains 'X-Smoke = "enabled"' "$CONFIG_PATH"
assert_file_contains 'smoke = "true"' "$CONFIG_PATH"

mct-agent config provider set deepseek \
  --url "$TEST_BASE_URL" \
  --api-key-env TEST_API_KEY \
  --clear-endpoint \
  --remove-header X-Smoke \
  --remove-query smoke \
  --no-interactive
assert_file_not_contains 'X-Smoke' "$CONFIG_PATH"
assert_file_not_contains 'smoke = "true"' "$CONFIG_PATH"
assert_file_not_contains 'endpoint = ' "$CONFIG_PATH"

mct-agent config provider rename deepseek deepseek-renamed --no-interactive
mct-agent config check
assert_file_contains 'provider = "deepseek-renamed"' "$CONFIG_PATH"
mct-agent config provider rename deepseek-renamed deepseek --no-interactive

mct-agent config provider add scratch-provider \
  --url https://invalid.example/v1 \
  --api-key smoke-placeholder-key \
  --endpoint /chat/completions \
  --header X-Scratch=yes \
  --query test=true \
  --no-interactive
mct-agent config provider show scratch-provider >"$SCRATCH_DIR/scratch-provider-show"
assert_file_contains '[redacted]' "$SCRATCH_DIR/scratch-provider-show"
mct-agent config provider set scratch-provider --clear-api-key --clear-endpoint \
  --remove-header X-Scratch --remove-query test --no-interactive
mct-agent config provider set scratch-provider --api-key-env TEST_API_KEY --no-interactive
mct-agent config provider remove scratch-provider --no-interactive

before=$(checksum "$CONFIG_PATH")
expect_exit 1 mct-agent config provider remove deepseek --no-interactive
assert_unchanged "$before"

echo "==> Exercising model list, show, add, set, rename, default, and remove..."
mct-agent config model list >"$SCRATCH_DIR/model-list"
assert_file_contains 'deepseek-primary' "$SCRATCH_DIR/model-list"
mct-agent config model show deepseek-primary >"$SCRATCH_DIR/model-show"
assert_file_contains "model: $TEST_MODEL" "$SCRATCH_DIR/model-show"

mct-agent config model add deepseek-scratch \
  --provider deepseek \
  --model "$TEST_MODEL" \
  --reasoning low \
  --param smoke=one \
  --param-json '{"temperature":0.1,"enabled":true}' \
  --no-interactive
assert_file_contains 'effort = "low"' "$CONFIG_PATH"
assert_file_contains 'temperature = 0.1' "$CONFIG_PATH"
assert_file_contains 'enabled = true' "$CONFIG_PATH"

for reasoning in medium high xhigh max provider-special; do
  mct-agent config model set deepseek-scratch --reasoning "$reasoning" --no-interactive
  assert_file_contains "effort = \"$reasoning\"" "$CONFIG_PATH"
done

before=$(checksum "$CONFIG_PATH")
expect_exit 2 mct-agent config model set deepseek-scratch --reasoning xhih --no-interactive
assert_unchanged "$before"

mct-agent config model set deepseek-scratch \
  --provider deepseek \
  --model "$TEST_MODEL" \
  --clear-reasoning \
  --remove-param smoke \
  --remove-param temperature \
  --remove-param enabled \
  --no-interactive
assert_file_not_contains 'provider-special' "$CONFIG_PATH"
assert_file_not_contains 'temperature = ' "$CONFIG_PATH"

mct-agent config model rename deepseek-scratch deepseek-review --no-interactive
mct-agent config model default deepseek-review --no-interactive
assert_file_contains 'default_model = "deepseek-review"' "$CONFIG_PATH"

before=$(checksum "$CONFIG_PATH")
expect_exit 2 mct-agent config model remove deepseek-review --no-interactive
assert_unchanged "$before"

mct-agent config model remove deepseek-review \
  --replacement deepseek-primary \
  --no-interactive
assert_file_contains 'default_model = "deepseek-primary"' "$CONFIG_PATH"
assert_file_not_contains 'deepseek-review' "$CONFIG_PATH"

before=$(checksum "$CONFIG_PATH")
expect_exit 1 mct-agent config model add deepseek-primary \
  --provider deepseek --model "$TEST_MODEL" --no-interactive
assert_unchanged "$before"
expect_exit 1 mct-agent config model set deepseek-primary \
  --provider missing-provider --no-interactive
assert_unchanged "$before"

echo "==> Exercising global and per-model cache commands..."
mct-agent config cache show >"$SCRATCH_DIR/cache-global-initial"
mct-agent config cache enable --no-interactive
mct-agent config cache set \
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

mct-agent config cache enable --model deepseek-primary --no-interactive
mct-agent config cache set --model deepseek-primary \
  --trigger-threshold 12288 \
  --lookback-offset 3 \
  --no-interactive
mct-agent config cache show --model deepseek-primary >"$SCRATCH_DIR/cache-model-override"
assert_file_contains '(model override)' "$SCRATCH_DIR/cache-model-override"
mct-agent config cache disable --model deepseek-primary --no-interactive
mct-agent config cache inherit --model deepseek-primary --no-interactive
mct-agent config cache show --model deepseek-primary >"$SCRATCH_DIR/cache-model-inherited"
assert_file_contains '(inherited)' "$SCRATCH_DIR/cache-model-inherited"
mct-agent config cache disable --no-interactive

before=$(checksum "$CONFIG_PATH")
expect_exit 2 mct-agent config cache set --trigger-threshold -1 --no-interactive
assert_unchanged "$before"

echo "==> Exercising config show, check, and failure safety..."
mct-agent config show >"$SCRATCH_DIR/config-show"
mct-agent config show --full >"$SCRATCH_DIR/config-show-full"
mct-agent config show --key models.deepseek-primary >"$SCRATCH_DIR/config-show-model"
assert_file_contains '********' "$SCRATCH_DIR/config-show-full"
mct-agent config check

before=$(checksum "$CONFIG_PATH")
expect_exit 2 mct-agent config add --provider incomplete --no-interactive
assert_unchanged "$before"
expect_exit 1 mct-agent config check --global --path "$SCRATCH_DIR/conflict.toml"
assert_unchanged "$before"
expect_exit 1 mct-agent config
assert_unchanged "$before"

echo "==> Exercising --path, --global, and MACHTIANI_CONFIG selection..."
path_config="$SCRATCH_DIR/path/config.toml"
env_config="$SCRATCH_DIR/environment/config.toml"
override_config="$SCRATCH_DIR/override/config.toml"
global_home="$SCRATCH_DIR/home"

mct-agent config add --path "$path_config" \
  --provider path-provider --url https://invalid.example/v1 \
  --api-key-env TEST_API_KEY --model path-model --alias path-model \
  --no-interactive
mct-agent config check --path "$path_config" >"$SCRATCH_DIR/path-check" 2>&1
assert_file_contains "Config file: $path_config" "$SCRATCH_DIR/path-check"

MACHTIANI_CONFIG="$env_config" mct-agent config add \
  --provider env-provider --url https://invalid.example/v1 \
  --api-key-env TEST_API_KEY --model env-model --alias env-model \
  --no-interactive
MACHTIANI_CONFIG="$env_config" mct-agent config check >"$SCRATCH_DIR/env-check" 2>&1
assert_file_contains "Config file: $env_config" "$SCRATCH_DIR/env-check"

MACHTIANI_CONFIG="$env_config" mct-agent config add --path "$override_config" \
  --provider override-provider --url https://invalid.example/v1 \
  --api-key-env TEST_API_KEY --model override-model --alias override-model \
  --no-interactive >"$SCRATCH_DIR/override-add" 2>&1
assert_file_contains 'ignoring MACHTIANI_CONFIG=' "$SCRATCH_DIR/override-add"
assert_file_contains "Config file: $override_config" "$SCRATCH_DIR/override-add"

HOME="$global_home" mct-agent config add --global \
  --provider global-provider --url https://invalid.example/v1 \
  --api-key-env TEST_API_KEY --model global-model --alias global-model \
  --no-interactive
test -f "$global_home/.machtiani/config.toml"
HOME="$global_home" mct-agent config check --global

echo "==> Verifying final DeepSeek configuration..."
mct-agent config check
mct-agent config provider list
mct-agent config provider show deepseek
mct-agent config model list
mct-agent config model show deepseek-primary
mct-agent config cache show

assert_file_contains 'default_model = "deepseek-primary"' "$CONFIG_PATH"
assert_file_contains 'api_key = "${TEST_API_KEY}"' "$CONFIG_PATH"
assert_file_contains 'cache_enabled = false' "$CONFIG_PATH"
assert_file_not_contains 'scratch-provider' "$CONFIG_PATH"
assert_file_not_contains 'deepseek-review' "$CONFIG_PATH"
assert_file_not_contains 'X-Smoke' "$CONFIG_PATH"
assert_file_not_contains 'endpoint = ' "$CONFIG_PATH"

echo "CONFIG CRUD SMOKE PASSED"
