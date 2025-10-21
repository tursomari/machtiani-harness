#!/bin/bash
set -euo pipefail

# Integration harness for validating internal README management against the undici fixture repository.
# Uses offline LLM stubs (`MCT_LLM_TEST_STUB`, `MCT_README_TEST_STUB`) so no network access is required.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}" )" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
MCT_SRC_DIR="$REPO_ROOT/agent"
UNDICI_SOURCE="$REPO_ROOT/tests/repositories/undici"
ARTIFACT_ROOT="$SCRIPT_DIR/artifacts/readme"
TMP_BASE="$SCRIPT_DIR/tmp"
mkdir -p "$TMP_BASE"
WORK_ROOT="$(mktemp -d "$TMP_BASE/run.XXXXXX")"

cleanup() {
  if [[ "${KEEP_README_TEST_TMP:-}" != "true" ]]; then
    rm -rf "$WORK_ROOT"
  else
    echo "Keeping temp workdir at $WORK_ROOT" >&2
  fi
}
trap cleanup EXIT

fail() {
  echo "[FAIL] $*" >&2
  exit 1
}

info() {
  echo "[INFO] $*" >&2
}

require_cmd() {
  local cmd="$1"
  command -v "$cmd" >/dev/null 2>&1 || fail "Missing required command: $cmd"
}

require_cmd git
require_cmd go

if [[ ! -e "$UNDICI_SOURCE/.git" ]]; then
  fail "Undici fixture repository missing at $UNDICI_SOURCE"
fi

info "Building mct-agent binary"
BIN_DIR="$WORK_ROOT/bin"
mkdir -p "$BIN_DIR"
MCT_BIN="$BIN_DIR/mct"
(
  cd "$MCT_SRC_DIR"
  GOCACHE="$WORK_ROOT/.gocache" go build -buildvcs=true -o "$MCT_BIN" ./cmd/mct-agent
) || fail "Go build failed"
chmod +x "$MCT_BIN"

export MCT_README_TEST_STUB="mock"
export MCT_LLM_TEST_STUB="stub-echo"
export OPENAI_API_KEY="${OPENAI_API_KEY:-stub-key}"
export OPENAI_BASE_URL="${OPENAI_BASE_URL:-https://example.com/api}"
export OPENAI_MODEL="${OPENAI_MODEL:-stub-model}"

info "Preparing undici working copy"
TEST_REPO="$WORK_ROOT/undici"
rm -rf "$TEST_REPO"
git clone --quiet "$UNDICI_SOURCE" "$TEST_REPO" || fail "git clone failed"
(
  cd "$TEST_REPO"
  git config user.name "README Bot"
  git config user.email "readme-bot@example.com"
)

HOME_OVERRIDE="$WORK_ROOT/home"
mkdir -p "$HOME_OVERRIDE"
export HOME="$HOME_OVERRIDE"

RESET_STATE() {
  rm -rf "$TEST_REPO/.machtiani"
}

ensure_readme_repo_exists() {
  if [[ ! -d "$TEST_REPO/.machtiani/artifacts/readme/.git" ]]; then
    fail "Expected readme repo to exist after run"
  fi
}

get_readme_repo() {
  echo "$TEST_REPO/.machtiani/artifacts/readme"
}

read_state_commit() {
  local repo
  repo="$(get_readme_repo)"
  local state_file="$repo/.state/last_project_commit"
  if [[ -f "$state_file" ]]; then
    tr -d '\n' < "$state_file"
  fi
}

get_commit_count() {
  local repo
  repo="$(get_readme_repo)"
  if [[ -d "$repo/.git" ]]; then
    git -C "$repo" rev-list --count HEAD 2>/dev/null || echo 0
  else
    echo 0
  fi
}

get_head_oid() {
  local repo
  repo="$(get_readme_repo)"
  if [[ -d "$repo/.git" && -s "$repo/.git/HEAD" ]]; then
    git -C "$repo" rev-parse HEAD 2>/dev/null || true
  fi
}

get_readme_content() {
  local repo
  repo="$(get_readme_repo)"
  if [[ -d "$repo/.git" ]]; then
    git -C "$repo" show HEAD:internal-readme.md 2>/dev/null || true
  fi
}

assert_tag_points() {
  local tag="$1" expected="$2"
  local repo
  repo="$(get_readme_repo)"
  local actual
  if ! actual=$(git -C "$repo" rev-parse "$tag^{}" 2>/dev/null); then
    fail "Missing expected tag $tag"
  fi
  if [[ "$actual" != "$expected" ]]; then
    fail "Tag $tag points to $actual, expected $expected"
  fi
}

assert_word_limit() {
  local content="$1" max_words="$2"
  local count
  count=$(echo "$content" | wc -w | awk '{print $1}')
  if [[ "$count" -gt "$max_words" ]]; then
    fail "README content exceeded $max_words words (got $count)"
  fi
}

run_case() {
  local label="$1" expect_regen="$2"
  info "Running case: $label"
  local project_commit
  project_commit=$(git -C "$TEST_REPO" rev-parse HEAD)

  local pre_count pre_head pre_content pre_state
  pre_count=$(get_commit_count)
  pre_head=$(get_head_oid)
  pre_content=$(get_readme_content)
  pre_state=$(read_state_commit)

  local case_dir="$ARTIFACT_ROOT/$label"
  mkdir -p "$case_dir"

  set +e
  (
    cd "$TEST_REPO"
    "$MCT_BIN" sync >"$case_dir/stdout.txt" 2>"$case_dir/stderr.txt"
  )
  local rc=$?
  set -e
  if [[ $rc -ne 0 ]]; then
    fail "mct command failed for $label (exit $rc)"
  fi

  ensure_readme_repo_exists

  local post_count post_head post_content post_state
  post_count=$(get_commit_count)
  post_head=$(get_head_oid)
  post_content=$(get_readme_content)
  post_state=$(read_state_commit)

  if [[ "$post_state" != "$project_commit" ]]; then
    fail "State tracking mismatch for $label: expected $project_commit, got ${post_state:-<none>}"
  fi

  if [[ "$expect_regen" == "true" ]]; then
    if [[ "$post_count" -ne $(( pre_count + 1 )) ]]; then
      fail "Expected new README commit for $label (pre=$pre_count post=$post_count)"
    fi
    if [[ -n "$pre_head" && "$post_head" == "$pre_head" ]]; then
      fail "Expected different HEAD in readme repo for $label"
    fi
    if [[ "$pre_content" == "$post_content" && -n "$pre_head" ]]; then
      fail "README content did not change for $label"
    fi
    if [[ -z "$post_content" ]]; then
      fail "Missing README content for $label"
    fi
    assert_word_limit "$post_content" 600
  else
    if [[ "$post_count" -ne "$pre_count" ]]; then
      fail "Unexpected new README commit for $label (pre=$pre_count post=$post_count)"
    fi
    if [[ "$post_head" != "$pre_head" ]]; then
      fail "Readme HEAD changed unexpectedly for $label"
    fi
    if [[ "$pre_content" != "$post_content" ]]; then
      fail "README content changed unexpectedly for $label"
    fi
  fi

  if [[ -n "$post_head" ]]; then
    assert_tag_points "oid-${project_commit}" "$post_head"
  fi

  info "Case $label passed"
}

rm -rf "$ARTIFACT_ROOT"
mkdir -p "$ARTIFACT_ROOT"
RESET_STATE

CASE1="db8e6422f3f3d4ff2dfd4742a0e39974618bdd8b"
CASE2="26004bfccaead3d6ec143c03a09dc036cea2c2ae"
CASE3="d28d5bab6c19615694a90361148ec36763b6d8c0"

info "Checkout initial commit $CASE1"
git -C "$TEST_REPO" checkout -q "$CASE1"
run_case "initial-generation" true

info "Checkout significant change commit $CASE2"
git -C "$TEST_REPO" checkout -q "$CASE2"
run_case "significant-change" true

run_case "repeated-commit" false

info "Creating docs-only commit"
git -C "$TEST_REPO" checkout -qb readme-docs-only
(
  cd "$TEST_REPO"
  echo "\n<!-- doc change to README -->" >> README.md
  git add README.md
  git commit -q -m "docs: update README"
)
DOC_COMMIT="$(git -C "$TEST_REPO" rev-parse HEAD)"
run_case "docs-only" false

info "Checkout latest commit $CASE3"
git -C "$TEST_REPO" checkout -q "$CASE3"
run_case "latest-progress" true

info "Verifying previous tags remain intact"
README_REPO="$(get_readme_repo)"
for tag in "oid-${CASE1}" "oid-${CASE2}" "oid-${DOC_COMMIT}"; do
  if ! git -C "$README_REPO" rev-parse "${tag}^{}" >/dev/null 2>&1; then
    fail "Expected tag ${tag} to exist"
  fi
done

info "All integration cases finished successfully"
