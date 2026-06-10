#!/usr/bin/env bash
set -euo pipefail

# ============================================================================
# ab-dev.sh — A/B build and compare for Docker images
# ============================================================================

usage() {
    cat <<'EOF'
Usage: ab-dev.sh [--cmd <command>] [--no-run] [--parse-results] <patch-file|git-ref>

Build two Docker images (control and treatment) from scripts/Dockerfile.build
and optionally run a command inside each container to compare outputs.

Arguments:
  <patch-file|git-ref>   Path to a patch file, or a git ref (e.g., HEAD~1,
                         origin/main).  If a git ref is provided, a patch is
                         generated from that ref to HEAD via "git diff".

Options:
  --cmd <command>        Command to run inside each container (sh -c).
                         Default: verify all built binaries report version/help.
  --no-run               Skip running containers; only build both images.
  --parse-results      Parse per-test-case PASS/FAIL from each container output
                        and print a side-by-side comparison table instead of
                        diff -u.
  --env KEY=VALUE       Forward KEY=VALUE into both containers via docker
                        run -e. May be specified multiple times. If --env
                        is not used, all TEST_* env vars from the host
                        are forwarded automatically.

Output:
  Container output is collected under /tmp/mct-ab-output/control/ and
  /tmp/mct-ab-output/treatment/.  A diff summary is printed to stdout.
EOF
    exit 1
}

# ----------------------------------------------------------------------------
# Setup
# ----------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

DOCKERFILE="$SCRIPT_DIR/Dockerfile.build"
CONTROL_IMAGE="mct-control"
TREATMENT_IMAGE="mct-treatment"
OUTPUT_BASE="/tmp/mct-ab-output"
CONTROL_OUT="$OUTPUT_BASE/control"
TREATMENT_OUT="$OUTPUT_BASE/treatment"

# Default command: verify all built binaries
DEFAULT_COMMAND='
set -euo pipefail
echo "=== Built binaries ==="
for bin in mct-agent mct file-discovery snippet-discovery shell-agent; do
    if command -v "$bin" &>/dev/null; then
        echo "--- $bin ---"
        "$bin" --version 2>&1 || "$bin" --help 2>&1 | head -5 || echo "(no version/help output)"
    else
        echo "--- $bin: NOT FOUND ---"
    fi
done
echo "=== Done ==="
'

# ----------------------------------------------------------------------------
# Parse arguments
# ----------------------------------------------------------------------------
INPUT=""
COMMAND="$DEFAULT_COMMAND"
NO_RUN=false
PARSE_RESULTS=false

EXTRA_ENV=()
ENV_EXPLICIT=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --cmd)
            COMMAND="$2"
            shift 2
            ;;
        --no-run)
            NO_RUN=true
            shift
            ;;
        --parse-results)
            PARSE_RESULTS=true
            shift
            ;;
        --help|-h)
            usage
            ;;
        --env)
            EXTRA_ENV+=("$2")
            ENV_EXPLICIT=true
            shift 2
            ;;
        -*)
            echo "Error: unknown option: $1" >&2
            usage
            ;;
        *)
            if [[ -z "${INPUT:-}" ]]; then
                INPUT="$1"
                shift
            else
                echo "Error: unexpected extra argument: $1" >&2
                usage
            fi
            ;;
    esac
done

if [[ -z "${INPUT:-}" ]]; then
    echo "Error: <patch-file|git-ref> argument is required" >&2
    usage
fi

# Default: forward all TEST_* env vars from host when --env not specified
if [[ "$ENV_EXPLICIT" == false ]]; then
    while IFS= read -r var; do
        EXTRA_ENV+=("$var=${!var}")
    done < <(compgen -v TEST_)
fi

# ----------------------------------------------------------------------------
# Resolve the patch file
# ----------------------------------------------------------------------------
PATCH_FILE=""
PATCH_TMP_DIR=""

cleanup_patch() {
    if [[ -n "${PATCH_TMP_DIR:-}" ]] && [[ -d "$PATCH_TMP_DIR" ]]; then
        rm -rf "$PATCH_TMP_DIR"
    fi
}
trap cleanup_patch EXIT

if [[ -f "$INPUT" ]]; then
    # Input is an existing file — use it directly as the patch
    PATCH_FILE="$(realpath "$INPUT")"
    echo "[patch] Using existing patch file: $PATCH_FILE"
else
    # Input is treated as a git ref — generate a patch via git diff
    echo "[patch] '$INPUT' is not a file; treating as git ref, generating patch..."

    # Determine the diff range: from <ref> to HEAD
    IN_RANGE="${INPUT}..HEAD"
    echo "[patch] Diff range: $IN_RANGE"

    PATCH_TMP_DIR="$(mktemp -d /tmp/mct-ab-patch.XXXXXX)"
    PATCH_FILE="$PATCH_TMP_DIR/change.patch"

    # Check if the ref exists
    if ! git -C "$REPO_ROOT" rev-parse --verify "$INPUT" >/dev/null 2>&1; then
        # Maybe it's a remote branch — try fetching
        echo "[patch] Ref '$INPUT' not found locally."

        # Try to see if it looks like a remote ref
        if [[ "$INPUT" == *"/"* ]]; then
            echo "[patch] Attempting to fetch remote for '$INPUT'..."
            git -C "$REPO_ROOT" fetch origin 2>/dev/null || true
        fi

        if ! git -C "$REPO_ROOT" rev-parse --verify "$INPUT" >/dev/null 2>&1; then
            echo "Error: git ref '$INPUT' could not be resolved" >&2
            exit 1
        fi
    fi

    # Generate the diff
    git -C "$REPO_ROOT" diff "$INPUT" -- > "$PATCH_FILE"

    if [[ ! -s "$PATCH_FILE" ]]; then
        echo "Warning: generated patch is empty (no diff between $INPUT and HEAD)" >&2
    else
        echo "[patch] Generated patch: $PATCH_FILE ($(wc -c < "$PATCH_FILE") bytes, $(wc -l < "$PATCH_FILE") lines)"
    fi
fi

# ----------------------------------------------------------------------------
# Build the control image (version A, no patch)
# ----------------------------------------------------------------------------
echo ""
echo "============================================================"
echo "  Step 1 — Build control image (version A, no patch)"
echo "============================================================"

echo "[build:control] Building $CONTROL_IMAGE from $REPO_ROOT..."

DOCKER_BUILDKIT=1 docker build \
    -f "$DOCKERFILE" \
    -t "$CONTROL_IMAGE" \
    "$REPO_ROOT"

echo "[build:control] Image built: $CONTROL_IMAGE"

# ----------------------------------------------------------------------------
# Build the treatment image (version B, with the change applied)
# ----------------------------------------------------------------------------
echo ""
echo "============================================================"
echo "  Step 2 — Build treatment image (version B, with patch)"
echo "============================================================"

echo "[build:treatment] Building $TREATMENT_IMAGE with base64-encoded patch..."

PATCH_B64="$(base64 -w0 "$PATCH_FILE")"

DOCKER_BUILDKIT=1 docker build \
    -f "$DOCKERFILE" \
    -t "$TREATMENT_IMAGE" \
    --build-arg "CHANGE_PATCH_B64=$PATCH_B64" \
    "$REPO_ROOT"

echo "[build:treatment] Image built: $TREATMENT_IMAGE"

# ----------------------------------------------------------------------------
# Run containers (unless --no-run)
# ----------------------------------------------------------------------------
if [[ "$NO_RUN" == true ]]; then
    echo ""
    echo "============================================================"
    echo "  --no-run specified; skipping container execution."
    echo "============================================================"
    echo "Control image:   $CONTROL_IMAGE"
    echo "Treatment image: $TREATMENT_IMAGE"
    exit 0
fi

echo ""
echo "============================================================"
# Build docker -e flags from EXTRA_ENV
ENV_FLAGS=()
for kv in "${EXTRA_ENV[@]}"; do
    ENV_FLAGS+=(-e "$kv")
done
if [[ ${#ENV_FLAGS[@]} -gt 0 ]]; then
    echo "[env] Forwarding ${#ENV_FLAGS[@]} env var(s) into containers: ${EXTRA_ENV[*]}"
fi
echo "  Step 3 — Run control container"
echo "============================================================"

rm -rf "$CONTROL_OUT"
mkdir -p "$CONTROL_OUT"

FORGE_MOUNT=""
if [[ -f "$HOME/.local/bin/forge" ]]; then
    FORGE_MOUNT="--volume $HOME/.local/bin/forge:/usr/local/bin/forge:ro"
else
    echo "[ab-dev.sh] WARNING: forge binary not found at $HOME/.local/bin/forge; mct-forge will fail if called inside the container."
fi

set +e
CONTROL_RC=0
docker run --rm \
    --volume "$CONTROL_OUT:/output:rw" \
    $FORGE_MOUNT \
    -e MACHTIANI_WORKSPACE_DEBUG=${MACHTIANI_WORKSPACE_DEBUG:-} \
    ${ENV_FLAGS[@]} \
    "$CONTROL_IMAGE" \
    -c "$COMMAND; echo \$? > /output/exit_code" > "$CONTROL_OUT/stdout.log" 2> "$CONTROL_OUT/stderr.log" || CONTROL_RC=$?
set -e

# Recover exit code from marker file; fall back to docker rc
if [[ -f "$CONTROL_OUT/exit_code" ]]; then
    CONTROL_RC="$(cat "$CONTROL_OUT/exit_code")"
fi

echo "[run:control] Exit code: $CONTROL_RC"
echo "[run:control] stdout -> $CONTROL_OUT/stdout.log ($(wc -c < "$CONTROL_OUT/stdout.log") bytes)"
echo "[run:control] stderr -> $CONTROL_OUT/stderr.log ($(wc -c < "$CONTROL_OUT/stderr.log") bytes)"

echo ""
echo "============================================================"
echo "  Step 4 — Run treatment container"
echo "============================================================"

rm -rf "$TREATMENT_OUT"
mkdir -p "$TREATMENT_OUT"

FORGE_MOUNT=""
if [[ -f "$HOME/.local/bin/forge" ]]; then
    FORGE_MOUNT="--volume $HOME/.local/bin/forge:/usr/local/bin/forge:ro"
else
    echo "[ab-dev.sh] WARNING: forge binary not found at $HOME/.local/bin/forge; mct-forge will fail if called inside the container."
fi

set +e
TREATMENT_RC=0
docker run --rm \
    --volume "$TREATMENT_OUT:/output:rw" \
    $FORGE_MOUNT \
    -e MACHTIANI_WORKSPACE_DEBUG=${MACHTIANI_WORKSPACE_DEBUG:-} \
    ${ENV_FLAGS[@]} \
    "$TREATMENT_IMAGE" \
    -c "$COMMAND; echo \$? > /output/exit_code" > "$TREATMENT_OUT/stdout.log" 2> "$TREATMENT_OUT/stderr.log" || TREATMENT_RC=$?
set -e

if [[ -f "$TREATMENT_OUT/exit_code" ]]; then
    TREATMENT_RC="$(cat "$TREATMENT_OUT/exit_code")"
fi

echo "[run:treatment] Exit code: $TREATMENT_RC"
echo "[run:treatment] stdout -> $TREATMENT_OUT/stdout.log ($(wc -c < "$TREATMENT_OUT/stdout.log") bytes)"
echo "[run:treatment] stderr -> $TREATMENT_OUT/stderr.log ($(wc -c < "$TREATMENT_OUT/stderr.log") bytes)"

# ----------------------------------------------------------------------------
# Diff and summary
# ----------------------------------------------------------------------------
echo ""
echo "============================================================"
echo "  Step 5 — Diff outputs"
echo "============================================================"

# Compare outputs
run_raw_diff() {
    echo ""
    echo "--- stdout diff (control vs treatment) ---"
    if diff -u "$CONTROL_OUT/stdout.log" "$TREATMENT_OUT/stdout.log"; then
        STDOUT_SAME=true
    else
        STDOUT_SAME=false
    fi

    echo ""
    echo "--- stderr diff (control vs treatment) ---"
    if diff -u "$CONTROL_OUT/stderr.log" "$TREATMENT_OUT/stderr.log"; then
        STDERR_SAME=true
    else
        STDERR_SAME=false
    fi
}

run_result_comparison() {
    local control_tsv="$OUTPUT_BASE/control.tsv"
    local treatment_tsv="$OUTPUT_BASE/treatment.tsv"
    local parse_failed=false

    set +e
    "$REPO_ROOT/agent/tests/ab-live.sh" --tsv-out "$control_tsv" "$CONTROL_OUT"
    local control_parse_rc=$?
    "$REPO_ROOT/agent/tests/ab-live.sh" --tsv-out "$treatment_tsv" "$TREATMENT_OUT"
    local treatment_parse_rc=$?
    set -e

    if [[ "$control_parse_rc" -ne 0 || "$treatment_parse_rc" -ne 0 ]]; then
        parse_failed=true
    fi

    if [[ "$parse_failed" == true ]]; then
        echo "[ab-dev.sh] WARNING: result parsing failed; falling back to raw diff -u." >&2
        run_raw_diff
        return 0
    fi

    declare -a control_order=()
    declare -a treatment_only_order=()
    declare -A control_status=()
    declare -A treatment_status=()
    declare -A seen_case=()

    local case_id status detail
    while IFS=$'\t' read -r case_id status detail || [[ -n "${case_id:-}" ]]; do
        if [[ "$case_id" == "case_id" && "$status" == "status" ]]; then
            continue
        fi
        if [[ -z "$case_id" ]]; then
            continue
        fi
        if [[ -z "${control_status[$case_id]+set}" ]]; then
            control_order+=("$case_id")
            seen_case[$case_id]=1
        fi
        control_status[$case_id]="$status"
    done < "$control_tsv"

    while IFS=$'\t' read -r case_id status detail || [[ -n "${case_id:-}" ]]; do
        if [[ "$case_id" == "case_id" && "$status" == "status" ]]; then
            continue
        fi
        if [[ -z "$case_id" ]]; then
            continue
        fi
        treatment_status[$case_id]="$status"
        if [[ -z "${seen_case[$case_id]+set}" ]]; then
            treatment_only_order+=("$case_id")
            seen_case[$case_id]=1
        fi
    done < "$treatment_tsv"

    local comparison_changed=false
    local regression_seen=false

    echo ""
    echo "--- per-test-case comparison (control vs treatment) ---"
    printf 'case_id\tcontrol_status\ttreatment_status\tdelta\n'

    compare_case() {
        local id="$1"
        local control="${control_status[$id]:-}"
        local treatment="${treatment_status[$id]:-}"
        local delta

        if [[ -z "$control" ]]; then
            delta="NEW"
        elif [[ -z "$treatment" ]]; then
            delta="MISSING"
        elif [[ "$control" == "$treatment" ]]; then
            delta="SAME"
        elif [[ "$control" == "PASS" && "$treatment" == "FAIL" ]]; then
            delta="REGRESSION"
        elif [[ "$control" == "FAIL" && "$treatment" == "PASS" ]]; then
            delta="FIX"
        elif [[ "$control" == "PASS" ]]; then
            delta="REGRESSION"
        elif [[ "$treatment" == "PASS" ]]; then
            delta="FIX"
        else
            delta="REGRESSION"
        fi

        if [[ "$delta" != "SAME" ]]; then
            comparison_changed=true
        fi
        if [[ "$delta" == "REGRESSION" ]]; then
            regression_seen=true
        fi

        printf '%s\t%s\t%s\t%s\n' "$id" "${control:-}" "${treatment:-}" "$delta"
    }

    local id
    for id in "${control_order[@]}"; do
        compare_case "$id"
    done
    for id in "${treatment_only_order[@]}"; do
        compare_case "$id"
    done

    if [[ "$comparison_changed" == true || "$regression_seen" == true ]]; then
        STDOUT_SAME=false
    else
        STDOUT_SAME=true
    fi
    STDERR_SAME=true
}

if [[ "$PARSE_RESULTS" == true ]]; then
    run_result_comparison
else
    run_raw_diff
fi

# ----------------------------------------------------------------------------
# Summary
# ----------------------------------------------------------------------------
echo ""
echo "============================================================"
echo "  Summary"
echo "============================================================"
echo ""
echo "  Control exit code:   $CONTROL_RC"
echo "  Treatment exit code: $TREATMENT_RC"
echo ""

if [[ "$CONTROL_RC" != "$TREATMENT_RC" ]]; then
    echo "  Exit codes DIFFER  (control=$CONTROL_RC, treatment=$TREATMENT_RC)"
    CHANGED=true
else
    echo "  Exit codes match   (both=$CONTROL_RC)"
    CHANGED=false
fi

if [[ "$STDOUT_SAME" == false ]]; then
    echo "  stdout: CHANGED relative to control"
    CHANGED=true
else
    echo "  stdout: unchanged"
fi

if [[ "$STDERR_SAME" == false ]]; then
    echo "  stderr: CHANGED relative to control"
    CHANGED=true
else
    echo "  stderr: unchanged"
fi

echo ""

if [[ "${CHANGED:-false}" == true ]]; then
    echo "  VERDICT: The treatment changed output relative to control."
else
    echo "  VERDICT: No differences detected — treatment matches control."
fi

echo ""
echo "  Output directories:"
echo "    Control:   $CONTROL_OUT"
echo "    Treatment: $TREATMENT_OUT"
echo ""

# Signal the overall result via exit code
if [[ "${CHANGED:-false}" == true ]]; then
    exit 1
else
    exit 0
fi
