#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: tests/tui/record-replay.sh [options]

Record a live mct-agent run and replay it from recorded LLM fixtures.

Options:
  --name NAME          Fixture name under .data/tui-replay (default: planner-shell-agent-cache)
  --prompt TEXT        Prompt text to run
  --prompt-file PATH   File containing prompt text
  --base-dir PATH      Artifact root (default: .data/tui-replay)
  --port PORT          Replay server port (default: first free port from 19876)
  --verbose            Record and replay mct-agent run --verbose
  --run-arg ARG        Append one mct-agent run argument in both phases (repeatable)
  --check-display-modes
                       Replay default, no-shell-steps, and focused run/resume modes
  --theme PROFILE      Set MACHTIANI_THEME for both phases
  --no-build           Do not build .data/bin/replay-server if replay-server is not on PATH
  -h, --help           Show this help

Artifacts are written under:
  <base-dir>/<name>/runs/<timestamp>/

Set MCT_AGENT_BIN to an explicit executable path to avoid PATH lookup. When it
is unset, the harness uses mct-agent from PATH. It records a PTY transcript
(*.terminal.log) as the whole terminal output, plus formatter capture
(*.tui.txt), process logs where applicable, LLM fixtures, replay config,
session ids, and a manifest.
USAGE
}

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
NAME="planner-shell-agent-cache"
BASE_DIR="$ROOT/.data/tui-replay"
PROMPT=""
PROMPT_FILE=""
PORT=""
BUILD=1
VERBOSE=0
THEME=""
CHECK_DISPLAY_MODES=0
RUN_ARG_VALUES=()

while (($# > 0)); do
  case "$1" in
    --name)
      NAME="${2:?missing value for --name}"
      shift 2
      ;;
    --prompt)
      PROMPT="${2:?missing value for --prompt}"
      shift 2
      ;;
    --prompt-file)
      PROMPT_FILE="${2:?missing value for --prompt-file}"
      shift 2
      ;;
    --base-dir)
      BASE_DIR="${2:?missing value for --base-dir}"
      shift 2
      ;;
    --port)
      PORT="${2:?missing value for --port}"
      shift 2
      ;;
    --verbose)
      VERBOSE=1
      shift
      ;;
    --run-arg)
      RUN_ARG_VALUES+=("${2:?missing value for --run-arg}")
      shift 2
      ;;
    --check-display-modes)
      CHECK_DISPLAY_MODES=1
      shift
      ;;
    --theme)
      THEME="${2:?missing value for --theme}"
      shift 2
      ;;
    --no-build)
      BUILD=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "$CHECK_DISPLAY_MODES" == "1" ]]; then
  for run_arg in "${RUN_ARG_VALUES[@]}"; do
    if [[ "$run_arg" == --focused || "$run_arg" == --focused=* || "$run_arg" == --no-shell-steps || "$run_arg" == --no-shell-steps=* ]]; then
      echo "error: display-mode checks provide their own --focused/--no-shell-steps flags" >&2
      exit 2
    fi
  done
fi

case "$THEME" in
  ""|terminal|machtiani-dark|machtiani-light|none) ;;
  *)
    echo "error: unknown theme '$THEME'" >&2
    exit 2
    ;;
esac

if [[ -n "$PROMPT" && -n "$PROMPT_FILE" ]]; then
  echo "error: use only one of --prompt or --prompt-file" >&2
  exit 2
fi

if [[ -n "$PROMPT_FILE" ]]; then
  PROMPT="$(cat "$PROMPT_FILE")"
fi

if [[ -z "$PROMPT" ]]; then
  DEFAULT_PROMPT="$BASE_DIR/$NAME/prompt.txt"
  if [[ -f "$DEFAULT_PROMPT" ]]; then
    PROMPT="$(cat "$DEFAULT_PROMPT")"
  else
    echo "error: provide --prompt or --prompt-file; no default prompt at $DEFAULT_PROMPT" >&2
    exit 2
  fi
fi

if ! command -v script >/dev/null 2>&1; then
  echo "error: script(1) is required for whole-terminal PTY capture" >&2
  exit 2
fi

BIN_DIR="$ROOT/.data/bin"
MCT_AGENT_BIN="${MCT_AGENT_BIN:-}"
if [[ -n "$MCT_AGENT_BIN" ]]; then
  if [[ "$MCT_AGENT_BIN" != /* ]]; then
    MCT_AGENT_BIN="$(cd "$(dirname "$MCT_AGENT_BIN")" && pwd)/$(basename "$MCT_AGENT_BIN")"
  fi
  MCT_AGENT_BIN="$(realpath "$MCT_AGENT_BIN" 2>/dev/null || printf '%s\n' "$MCT_AGENT_BIN")"
else
  MCT_AGENT_BIN="$(command -v mct-agent || true)"
fi
REPLAY_SERVER_BIN="$(command -v replay-server || true)"
if [[ -z "$REPLAY_SERVER_BIN" ]]; then
  REPLAY_SERVER_BIN="$BIN_DIR/replay-server"
fi

if [[ -z "$MCT_AGENT_BIN" || ! -x "$MCT_AGENT_BIN" ]]; then
  echo "error: set MCT_AGENT_BIN to an executable or make mct-agent available on PATH" >&2
  exit 2
fi

if [[ "$BUILD" == "1" && ! -x "$REPLAY_SERVER_BIN" ]]; then
  mkdir -p "$BIN_DIR" "$ROOT/.data/gocache"
  (
    cd "$ROOT/agent"
    GOCACHE="$ROOT/.data/gocache" go build -o "$BIN_DIR/replay-server" ./cmd/replay-server
  )
  REPLAY_SERVER_BIN="$BIN_DIR/replay-server"
fi

if [[ ! -x "$REPLAY_SERVER_BIN" ]]; then
  echo "error: missing executable $REPLAY_SERVER_BIN; install replay-server or rerun without --no-build" >&2
  exit 2
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
RUN_DIR="$BASE_DIR/$NAME/runs/$timestamp"
mkdir -p "$RUN_DIR"
printf '%s\n' "$PROMPT" > "$RUN_DIR/prompt.txt"
"$MCT_AGENT_BIN" --version > "$RUN_DIR/mct-agent.version.txt" 2>&1 || true
{
  printf 'mct_agent_path=%s\n' "$MCT_AGENT_BIN"
  printf 'replay_server_path=%s\n' "$REPLAY_SERVER_BIN"
} > "$RUN_DIR/binaries.txt"

shell_quote_command() {
  local out=""
  local arg
  for arg in "$@"; do
    printf -v out '%s%q ' "$out" "$arg"
  done
  printf '%s' "${out% }"
}

run_under_pty() {
  local terminal_log="$1"
  shift
  local command
  command="$(shell_quote_command "$@")"
  script -q -f -e -c "$command" "$terminal_log"
}

count_matches() {
  local pattern="$1"
  local file="$2"
  if [[ ! -f "$file" ]]; then
    printf '0'
    return
  fi
  rg -c "$pattern" "$file" 2>/dev/null || printf '0'
}

line_count() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    printf '0'
    return
  fi
  wc -l < "$file"
}

session_from_log() {
  local file="$1"
  if [[ ! -f "$file" ]]; then
    return
  fi
  awk '
    /^Session ID: / {gsub(/\r/, "", $NF); print $NF; exit}
    /^Session: / {gsub(/\r/, "", $2); print $2; exit}
	/mct-agent run.*--session agent-[0-9TZ]+-[0-9]+/ {
	  for (i = 1; i <= NF; i++) {
	    if ($i == "--session" && (i + 1) <= NF && $(i + 1) ~ /^agent-[0-9TZ]+-[0-9]+$/) {
	      gsub(/\r/, "", $(i + 1)); print $(i + 1); exit
	    }
	  }
	}
    /session agent-[0-9TZ]+-[0-9]+/ {
      for (i = 1; i <= NF; i++) {
        if ($i == "session" && (i + 1) <= NF && $(i + 1) ~ /^agent-[0-9TZ]+-[0-9]+$/) {
          gsub(/\r/, "", $(i + 1)); print $(i + 1); exit
        }
      }
    }
  ' "$file"
}

is_port_open() {
  local port="$1"
  (echo >"/dev/tcp/127.0.0.1/$port") >/dev/null 2>&1
}

find_free_port() {
  local candidate="${1:-19876}"
  while is_port_open "$candidate"; do
    candidate=$((candidate + 1))
  done
  printf '%s' "$candidate"
}

wait_for_port() {
  local port="$1"
  local deadline=$((SECONDS + 10))
  while ((SECONDS < deadline)); do
    if is_port_open "$port"; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

sanitize_config_for_replay() {
  local src="$1"
  local dst="$2"
  local port="$3"
  sed -E \
    -e "s#^([[:space:]]*base_url[[:space:]]*=[[:space:]]*\").*(\")#\\1http://127.0.0.1:${port}\\2#" \
    -e 's#^([[:space:]]*api_key[[:space:]]*=[[:space:]]*\").*(\")#\1dummy\2#' \
    "$src" > "$dst"
  perl -0pi -e 's/sk\.\./dummy/g' "$dst"
}

normalize_capture() {
  local src="$1"
  local dst="$2"
  perl -pe 's/\e(?:\[[0-9;?]*[ -\/]*[@-~]|[A-Za-z])//g; s/\r//g' "$src" > "$dst"
}

require_capture_match() {
  local pattern="$1"
  local file="$2"
  local description="$3"
  if ! rg -q "$pattern" "$file"; then
    echo "display-mode check failed: $description ($file)" >&2
    return 1
  fi
}

reject_capture_match() {
  local pattern="$1"
  local file="$2"
  local description="$3"
  if rg -q "$pattern" "$file"; then
    echo "display-mode check failed: $description ($file)" >&2
    return 1
  fi
}

assert_display_mode_captures() {
  local prefix
  for prefix in live replay replay-no-shell-steps replay-focused resume resume-no-shell-steps resume-focused; do
    normalize_capture "$RUN_DIR/$prefix.tui.txt" "$RUN_DIR/$prefix.tui.plain.txt"
  done

  local live="$RUN_DIR/live.tui.plain.txt"
  local default="$RUN_DIR/replay.tui.plain.txt"
  local no_shell="$RUN_DIR/replay-no-shell-steps.tui.plain.txt"
  local focused="$RUN_DIR/replay-focused.tui.plain.txt"
  local resume_default="$RUN_DIR/resume.tui.plain.txt"
  local resume_no_shell="$RUN_DIR/resume-no-shell-steps.tui.plain.txt"
  local resume_focused="$RUN_DIR/resume-focused.tui.plain.txt"
  local action_step='^Step [0-9]+( of [0-9]+)?$'
  local action_command='^\$ [^[:space:]]'
  local conclusion='Resume this session:|SHELL-AGENT INTERRUPTED|USER INPUT NEEDED'

  # The unflagged live and replay captures establish the pre-flag visual shape.
  for file in "$live" "$default"; do
    require_capture_match 'machtiani \(mct\)' "$file" "default mode lost the banner"
    require_capture_match "$conclusion" "$file" "default mode lost the conclusion"
    require_capture_match "$action_step" "$file" "default mode lost shell step blocks"
    require_capture_match "$action_command" "$file" "default mode lost shell command blocks"
    require_capture_match 'session token input' "$file" "default mode lost the footer"
  done

  require_capture_match 'machtiani \(mct\)' "$no_shell" "no-shell-steps lost the banner"
  require_capture_match "$conclusion" "$no_shell" "no-shell-steps lost the conclusion"
  require_capture_match 'session token input' "$no_shell" "no-shell-steps lost the footer"
  reject_capture_match "$action_step" "$no_shell" "no-shell-steps rendered a step block"
  reject_capture_match "$action_command" "$no_shell" "no-shell-steps rendered a command block"

  require_capture_match 'machtiani \(mct\)' "$focused" "focused mode lost the banner"
  require_capture_match "$conclusion" "$focused" "focused mode lost the conclusion"
  reject_capture_match "$action_step" "$focused" "focused mode rendered a step block"
  reject_capture_match "$action_command" "$focused" "focused mode rendered a command block"
  reject_capture_match 'session token input' "$focused" "focused mode rendered the footer"
  reject_capture_match 'Resuming session|\[resume\]' "$focused" "focused mode rendered a resume notice"

  for file in "$resume_default" "$resume_no_shell" "$resume_focused"; do
    require_capture_match 'machtiani \(mct\)' "$file" "resume mode lost the banner"
    require_capture_match "$conclusion" "$file" "resume mode lost the conclusion"
  done
  require_capture_match 'Resuming session' "$resume_default" "default resume lost its resume notice"
  require_capture_match 'session token input' "$resume_default" "default resume lost the footer"
  require_capture_match 'Resuming session' "$resume_no_shell" "no-shell-steps resume lost its resume notice"
  require_capture_match 'session token input' "$resume_no_shell" "no-shell-steps resume lost the footer"
  reject_capture_match "$action_step" "$resume_no_shell" "no-shell-steps resume rendered a step block"
  reject_capture_match "$action_command" "$resume_no_shell" "no-shell-steps resume rendered a command block"
  require_capture_match "$conclusion" "$resume_focused" "focused resume lost the conclusion"
  reject_capture_match "$action_step" "$resume_focused" "focused resume rendered a step block"
  reject_capture_match "$action_command" "$resume_focused" "focused resume rendered a command block"
  reject_capture_match 'session token input' "$resume_focused" "focused resume rendered the footer"
  reject_capture_match 'Resuming session|\[resume\]' "$resume_focused" "focused resume rendered a resume notice"

  diff -u "$live" "$default" > "$RUN_DIR/display-default-baseline.diff" || true
  diff -u "$default" "$no_shell" > "$RUN_DIR/display-run-no-shell-steps.diff" || true
  diff -u "$default" "$focused" > "$RUN_DIR/display-run-focused.diff" || true
  diff -u "$resume_default" "$resume_no_shell" > "$RUN_DIR/display-resume-no-shell-steps.diff" || true
  diff -u "$resume_default" "$resume_focused" > "$RUN_DIR/display-resume-focused.diff" || true
  for file in \
    "$RUN_DIR/display-run-no-shell-steps.diff" \
    "$RUN_DIR/display-run-focused.diff" \
    "$RUN_DIR/display-resume-no-shell-steps.diff" \
    "$RUN_DIR/display-resume-focused.diff"; do
    if [[ ! -s "$file" ]]; then
      echo "display-mode check failed: expected a non-empty formatter diff ($file)" >&2
      return 1
    fi
  done
}

write_manifest() {
  local live_session replay_session
  live_session="$(cat "$RUN_DIR/live-session-id.txt" 2>/dev/null || true)"
  replay_session="$(cat "$RUN_DIR/replay-session-id.txt" 2>/dev/null || true)"

  {
    printf 'run_dir=%s\n' "$RUN_DIR"
    printf 'mct_agent_path=%s\n' "$MCT_AGENT_BIN"
    printf 'replay_server_path=%s\n' "$REPLAY_SERVER_BIN"
    printf 'verbose=%s\n' "$VERBOSE"
    printf 'theme=%s\n' "${THEME:-config}"
    printf 'run_arg_count=%s\n' "${#RUN_ARG_VALUES[@]}"
    printf 'display_mode_check=%s\n' "$CHECK_DISPLAY_MODES"
    printf 'live_session=%s\n' "$live_session"
    printf 'replay_session=%s\n' "$replay_session"
    printf 'fixture_count=%s\n' "$(line_count "$RUN_DIR/live.llm-fixtures.jsonl")"
    printf 'live_terminal_lines=%s\n' "$(line_count "$RUN_DIR/live.terminal.log")"
    printf 'replay_terminal_lines=%s\n' "$(line_count "$RUN_DIR/replay.terminal.log")"
    printf 'live_tui_lines=%s\n' "$(line_count "$RUN_DIR/live.tui.txt")"
    printf 'replay_tui_lines=%s\n' "$(line_count "$RUN_DIR/replay.tui.txt")"
    printf 'live_terminal_raw_actions=%s\n' "$(count_matches '^MCT_SHELL_ACTION' "$RUN_DIR/live.terminal.log")"
    printf 'replay_terminal_raw_actions=%s\n' "$(count_matches '^MCT_SHELL_ACTION' "$RUN_DIR/replay.terminal.log")"
    printf 'live_tui_shell_step_lines=%s\n' "$(count_matches '\[shell step ' "$RUN_DIR/live.tui.txt")"
    printf 'replay_tui_shell_step_lines=%s\n' "$(count_matches '\[shell step ' "$RUN_DIR/replay.tui.txt")"
    if [[ -n "$live_session" && -f "$ROOT/.machtiani/sessions/$live_session/trajectory/agent.jsonl" ]]; then
      printf 'live_trajectory_actions=%s\n' "$(count_matches '"kind":"shell-agent.action"' "$ROOT/.machtiani/sessions/$live_session/trajectory/agent.jsonl")"
    else
      printf 'live_trajectory_actions=0\n'
    fi
    if [[ -n "$replay_session" && -f "$ROOT/.machtiani/sessions/$replay_session/trajectory/agent.jsonl" ]]; then
      printf 'replay_trajectory_actions=%s\n' "$(count_matches '"kind":"shell-agent.action"' "$ROOT/.machtiani/sessions/$replay_session/trajectory/agent.jsonl")"
    else
      printf 'replay_trajectory_actions=0\n'
    fi
    if [[ -n "$live_session" && -f "$ROOT/.machtiani/sessions/$live_session/chat/agent-final-answer.md" ]]; then
      printf 'live_final_answer_sha256=%s\n' "$(sha256sum "$ROOT/.machtiani/sessions/$live_session/chat/agent-final-answer.md" | awk '{print $1}')"
    fi
    if [[ -n "$replay_session" && -f "$ROOT/.machtiani/sessions/$replay_session/chat/agent-final-answer.md" ]]; then
      printf 'replay_final_answer_sha256=%s\n' "$(sha256sum "$ROOT/.machtiani/sessions/$replay_session/chat/agent-final-answer.md" | awk '{print $1}')"
    fi
    if [[ "$CHECK_DISPLAY_MODES" == "1" ]]; then
      local prefix
      for prefix in replay-no-shell-steps replay-focused resume resume-no-shell-steps resume-focused; do
        if [[ -f "$RUN_DIR/$prefix.exit" ]]; then
          printf '%s_exit=%s\n' "${prefix//-/_}" "$(sed -n 's/^exit=//p' "$RUN_DIR/$prefix.exit")"
        fi
      done
    fi
  } > "$RUN_DIR/manifest.txt"
}

echo "run_dir=$RUN_DIR"

RUN_ARGS=(run --text "$PROMPT")
if [[ "$VERBOSE" == "1" ]]; then
  RUN_ARGS+=(--verbose)
fi
RUN_ARGS+=("${RUN_ARG_VALUES[@]}")
LIVE_ENV=(
  "MACHTIANI_TUI_CAPTURE=$RUN_DIR/live.tui.txt"
  "LLM_RECORD_FIXTURES=$RUN_DIR/live.llm-fixtures.jsonl"
)
if [[ -n "$THEME" ]]; then
  LIVE_ENV+=("MACHTIANI_THEME=$THEME")
fi

set +e
run_under_pty "$RUN_DIR/live.terminal.log" \
  env \
  "${LIVE_ENV[@]}" \
  "$MCT_AGENT_BIN" "${RUN_ARGS[@]}"
live_exit=$?
set -e
printf 'exit=%s\n' "$live_exit" > "$RUN_DIR/live.exit"
session_from_log "$RUN_DIR/live.terminal.log" > "$RUN_DIR/live-session-id.txt"

if [[ "$live_exit" != "0" ]]; then
  write_manifest
  echo "live run failed; see $RUN_DIR/live.terminal.log" >&2
  exit "$live_exit"
fi

PORT="$(find_free_port "${PORT:-19876}")"
printf '%s\n' "$PORT" > "$RUN_DIR/replay-port.txt"

config_src="${MACHTIANI_CONFIG:-$ROOT/.machtiani/config.toml}"
if [[ ! -f "$config_src" ]]; then
  echo "error: config not found: $config_src" >&2
  exit 2
fi
sanitize_config_for_replay "$config_src" "$RUN_DIR/replay.config.toml" "$PORT"

server_pid=""
cleanup() {
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" >/dev/null 2>&1; then
    kill "$server_pid" >/dev/null 2>&1 || true
    wait "$server_pid" >/dev/null 2>&1 || true
  fi
  server_pid=""
}
trap cleanup EXIT

run_replay_case() {
  local prefix="$1"
  shift
  local server_log="$RUN_DIR/$prefix-server.log"
  local terminal_log="$RUN_DIR/$prefix.terminal.log"
  local tui_capture="$RUN_DIR/$prefix.tui.txt"

  "$REPLAY_SERVER_BIN" -fixtures "$RUN_DIR/live.llm-fixtures.jsonl" -port "$PORT" \
    > "$server_log" 2>&1 &
  server_pid=$!
  if ! wait_for_port "$PORT"; then
    echo "error: replay server did not start on port $PORT" >&2
    cat "$server_log" >&2 || true
    cleanup
    return 1
  fi

  local -a replay_env=(
    "MACHTIANI_CONFIG=$RUN_DIR/replay.config.toml"
    "MACHTIANI_TUI_CAPTURE=$tui_capture"
  )
  if [[ -n "$THEME" ]]; then
    replay_env+=("MACHTIANI_THEME=$THEME")
  fi
  run_under_pty "$terminal_log" \
    env \
    "${replay_env[@]}" \
    "$MCT_AGENT_BIN" "$@"
  local case_exit=$?
  printf 'exit=%s\n' "$case_exit" > "$RUN_DIR/$prefix.exit"
  session_from_log "$terminal_log" > "$RUN_DIR/$prefix-session-id.txt"
  cleanup
  return "$case_exit"
}

set +e
run_replay_case replay "${RUN_ARGS[@]}"
replay_exit=$?
set -e

display_exit=0
if [[ "$CHECK_DISPLAY_MODES" == "1" && "$replay_exit" == "0" ]]; then
  if [[ "$display_exit" == "0" ]]; then
    set +e
    run_replay_case replay-no-shell-steps "${RUN_ARGS[@]}" --no-shell-steps
    no_shell_exit=$?
    run_replay_case replay-focused "${RUN_ARGS[@]}" --focused
    focused_exit=$?
    set -e
    if [[ "$no_shell_exit" != "0" || "$focused_exit" != "0" ]]; then
      display_exit=1
    fi
  fi

  if [[ "$display_exit" == "0" ]]; then
    default_session="$(cat "$RUN_DIR/replay-session-id.txt")"
    no_shell_session="$(cat "$RUN_DIR/replay-no-shell-steps-session-id.txt")"
    focused_session="$(cat "$RUN_DIR/replay-focused-session-id.txt")"
    if [[ -z "$default_session" || -z "$no_shell_session" || -z "$focused_session" ]]; then
      echo "error: unable to extract sessions for display-mode resume checks" >&2
      display_exit=1
    fi
  fi

  if [[ "$display_exit" == "0" ]]; then
    RESUME_COMMON_ARGS=()
    if [[ "$VERBOSE" == "1" ]]; then
      RESUME_COMMON_ARGS+=(--verbose)
    fi
    RESUME_COMMON_ARGS+=("${RUN_ARG_VALUES[@]}")

    set +e
    run_replay_case resume resume "$default_session" "${RESUME_COMMON_ARGS[@]}"
    resume_exit=$?
    run_replay_case resume-no-shell-steps resume "$no_shell_session" "${RESUME_COMMON_ARGS[@]}" --no-shell-steps
    resume_no_shell_exit=$?
    run_replay_case resume-focused resume "$focused_session" "${RESUME_COMMON_ARGS[@]}" --focused
    resume_focused_exit=$?
    set -e
    if [[ "$resume_exit" != "0" || "$resume_no_shell_exit" != "0" || "$resume_focused_exit" != "0" ]]; then
      display_exit=1
    fi
  fi

  if [[ "$display_exit" == "0" ]]; then
    if assert_display_mode_captures; then
      printf 'pass\n' > "$RUN_DIR/display-mode-check.txt"
    else
      printf 'fail\n' > "$RUN_DIR/display-mode-check.txt"
      display_exit=1
    fi
  fi
fi

cleanup
trap - EXIT

write_manifest
cat "$RUN_DIR/manifest.txt"

if [[ "$display_exit" != "0" ]]; then
  exit "$display_exit"
fi
exit "$replay_exit"
