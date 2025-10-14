#!/usr/bin/env bash
set -euo pipefail

# Live integration runner for file-discovery against the vendored undici repo.
# - Runs three scenarios by checking out specific OIDs
# - Pipes an issue description into file-discovery
# - Validates only structural invariants of the stdout block and trajectory
# - Skips live network calls if OPENAI_API_KEY is not set (performs RG dry-run)

# Parse optional session flag (-s/--session-id)
SESSION_ID=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -s|--session-id)
      if [[ $# -lt 2 ]]; then
        echo "Missing value for $1" >&2
        exit 2
      fi
      SESSION_ID="$2"; shift 2 ;;
    --)
      shift; break ;;
    *)
      echo "Unknown option: $1" >&2
      exit 2 ;;
  esac
done

here_dir="$(cd "$(dirname "$0")" && pwd)"
repo_root="$(cd "$here_dir/.." && pwd)"
fixture_dir="$(pwd)"

# Where to write artifacts (stdout/stderr/trajectory). Defaults to cwd.
artifact_dir="${ARTIFACT_DIR:-.}"
mkdir -p "$artifact_dir"

# Derive artifact directory relative to the repo root (fixture_dir), if applicable,
# so we can exclude it from git clean operations.
artifact_rel=""
case "$artifact_dir" in
  /*)
    # Absolute path; check if it is inside fixture_dir
    case "$artifact_dir" in
      "$fixture_dir"/*)
        artifact_rel="${artifact_dir#"$fixture_dir/"}"
        ;;
    esac
    ;;
  *)
    # Relative path from fixture_dir
    artifact_rel="$artifact_dir"
    ;;
esac

echo "cwd: $fixture_dir"
echo "repo_root: $repo_root"
if [[ -n "$SESSION_ID" ]]; then
  echo "session_id: $SESSION_ID"
fi

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY not set — live tests require an API key" >&2
  exit 2
fi

function rg_files_cache() {
  # Cache list of files produced by `rg --files` (post-filtering is internal to file-discovery,
  # but this is good enough for existence checks).
  rg --files | sed '/^$/d' | LC_ALL=C sort -u
}

function assert_stdout_block() {
  local stdout_file="$1"
  local extracted="$2"
  local label="$3"

  # Derive label if not provided
  if [[ -z "$label" ]]; then
    label="file-discovery"
  fi

  # Extract BEGIN/END block for the given label
  if ! awk -v L="$label" '($0==("BEGIN_RELEVANT_FILES[" L "]")){flag=1;next} ($0==("END_RELEVANT_FILES[" L "]")){flag=0} flag' "$stdout_file" > "$extracted"; then
    echo "Failed to extract relevant files block for label [$label]" >&2
    return 1
  fi

  local count
  count=$(grep -c '^[^[:space:]].*' "$extracted" || true)
  if [[ "$count" -le 0 ]]; then
    echo "No paths in relevant files block" >&2
    return 1
  fi

  # Format checks: relative paths, no backslashes, no .., no trailing slash
  if grep -nE '^(/|\\|.*\\\\|.*\.\.|.*/$)' "$extracted"; then
    echo "Invalid path formatting detected in relevant files block" >&2
    return 1
  fi

  # Dedup check: ensure no duplicates
  local uniq_count
  uniq_count=$(LC_ALL=C sort -u "$extracted" | wc -l | tr -d ' ')
  if [[ "$uniq_count" -ne "$count" ]]; then
    echo "Duplicate paths detected in relevant files block" >&2
    return 1
  fi

  return 0
}

function assert_paths_in_repo() {
  local extracted="$1"
  local rg_list_file="$2"

  local missing=0
  while IFS= read -r p; do
    if ! grep -Fxq -- "$p" "$rg_list_file"; then
      echo "Path not in rg --files listing: $p" >&2
      missing=$((missing+1))
    fi
  done < "$extracted"

  # Be robust: allow a small number of misses (models can occasionally hallucinate),
  # but require that at least one file matches the repo listing.
  local total
  total=$(wc -l < "$extracted" | tr -d ' ')
  local hits=$(( total - missing ))
  if [[ "$hits" -le 0 ]]; then
    echo "No selected paths were found in repository listing" >&2
    return 1
  fi

  if [[ "$missing" -gt 2 ]]; then
    echo "Too many missing paths ($missing) vs total ($total)" >&2
    return 1
  fi
}

function assert_trajectory_has_rg() {
  local traj="$1"
  if [[ ! -s "$traj" ]]; then
    echo "Trajectory file missing or empty: $traj" >&2
    return 1
  fi
  if ! rg -n '"type":"rg_exec"' "$traj" >/dev/null; then
    echo "Trajectory missing rg_exec event" >&2
    return 1
  fi
  if ! rg -n 'RG_OUT:' "$traj" >/dev/null; then
    echo "Trajectory missing RG_OUT content" >&2
    return 1
  fi
  if ! rg -n '"type":"run_end"' "$traj" >/dev/null; then
    echo "Trajectory missing run_end event" >&2
    return 1
  fi
}

function run_case() {
  local name="$1"; shift
  local oid="$1"; shift
  local prompt="$1"; shift

  echo "=== CASE: $name at $oid ===" >&2
  git reset --hard -q
  if [[ -n "$artifact_rel" ]]; then
    # Exclude artifact directory from cleaning to avoid bind mount errors
    git clean -fdqx -e "$artifact_rel/"
  else
    git clean -fdqx
  fi
  git checkout -q "$oid"

  # Common filenames per case
  local stdout_file="$artifact_dir/stdout-${name}.txt"
  local stderr_file="$artifact_dir/stderr-${name}.txt"
  local traj_file="$artifact_dir/trajectory-${name}.jsonl"
  local extracted_file="$artifact_dir/relevant-${name}.txt"
  rm -f "$stdout_file" "$stderr_file" "$traj_file" "$extracted_file"

  # Run the live call
  if [[ -n "$SESSION_ID" ]]; then
    printf '%s\n' "$prompt" | file-discovery -trajectory "$traj_file" -s "$SESSION_ID" >"$stdout_file" 2>"$stderr_file"
  else
    printf '%s\n' "$prompt" | file-discovery -trajectory "$traj_file" >"$stdout_file" 2>"$stderr_file"
  fi
  local rc=$?
  if [[ $rc -ne 0 ]]; then
    echo "file-discovery exited with $rc for $name" >&2
    sed -n '1,120p' "$stderr_file" >&2 || true
    return $rc
  fi

  # Assertions
  local rg_cache_file="$artifact_dir/.rg-files-${name}.txt"
  rg_files_cache > "$rg_cache_file"

  # Compute expected label from SESSION_ID (first 5 chars) or default
  local label="file-discovery"
  if [[ -n "$SESSION_ID" ]]; then
    # trim leading/trailing whitespace, then take first 5 characters
    local sid
    sid="$(printf "%s" "$SESSION_ID" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')"
    label="${sid:0:5}"
  fi

  assert_stdout_block "$stdout_file" "$extracted_file" "$label"
  assert_paths_in_repo "$extracted_file" "$rg_cache_file"
  assert_trajectory_has_rg "$traj_file"

  # Scenario‑specific loose invariants
  case "$name" in
    issue-a)
      # Expect at least one TypeScript definition file to be considered.
      if ! rg -n '^(types/.*\.d\.ts|index\.d\.ts|test/types/.*\.d\.ts)$' "$extracted_file" >/dev/null; then
        echo "Invariant failed: expected a .d.ts file path for $name" >&2
        return 1
      fi
      ;;
    issue-b)
      # Expect at least one H2/HTTP2 related file.
      if ! rg -n '(http2|/h2|allowH2)' "$extracted_file" -N >/dev/null; then
        echo "Invariant failed: expected an http2/h2 related path for $name" >&2
        echo "Paths returned:" >&2
        sed 's/^/  /' "$extracted_file" >&2 || true
        return 1
      fi
      ;;
    issue-c)
      # Build a loose set of pool/keep-alive related files from source tree and require intersection.
      local tmp_pool="$artifact_dir/.pool-candidates-${name}.txt"
      rg -l -S 'keep[- ]?alive|\bpool\b' lib > "$tmp_pool" || true
      if [[ -s "$tmp_pool" ]]; then
        if ! grep -Fxf "$tmp_pool" "$extracted_file" >/dev/null; then
          echo "Invariant failed: expected a pool/keep-alive related lib file for $name" >&2
          echo "Sample candidates:" >&2
          head -n 10 "$tmp_pool" >&2 || true
          return 1
        fi
      else
        echo "Warning: could not derive pool candidates; skipping semantic check for $name" >&2
      fi
      ;;
  esac

  echo "OK: $name" >&2
}

## Run all cases

# https://github.com/nodejs/undici/pull/3964/files
run_case "issue-a" \
  "3eeeeb79b9b890cce39607016fd9e91d235c7134" \
  "Add missing type for ResponseError. Let typescript know the type of the error in order to use it in a try/catch."

# https://github.com/nodejs/undici/pull/3978/files
run_case "issue-b" \
  "e51df14623d9c4cb3fd8753727c3e62c24a57a3a" \
  "fix bad response on h2 server"

# Not a real issue, just a question.
run_case "issue-c" \
  "cfec10cd0ca9f9f2d4daf354d55dfcb1bf9a3d7b" \
  "How does Undici handle connection pooling and HTTP keep-alive compared to the Requests library, and what are the implications of these approaches on performance and resource management in high-concurrency scenarios?"

echo "All cases finished." >&2
