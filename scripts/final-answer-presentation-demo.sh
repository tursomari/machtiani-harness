#!/usr/bin/env bash

set -euo pipefail

theme="terminal"
glyph_mode="unicode"
outcome="complete"

usage() {
  printf '%s\n' 'Usage: final-answer-presentation-demo.sh [--theme terminal|machtiani-dark|machtiani-light|none] [--glyphs unicode|ascii] [--outcome complete|interrupted|input]'
}

while (($# > 0)); do
  case "$1" in
    --theme)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      theme="$2"
      shift 2
      ;;
    --glyphs)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      glyph_mode="$2"
      shift 2
      ;;
    --outcome)
      [[ $# -ge 2 ]] || { usage >&2; exit 2; }
      outcome="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

reset=$'\033[0m'
bold=$'\033[1m'
underline=$'\033[4m'

case "$theme" in
  terminal)
    truth=$'\033[36m'
    goodness=$'\033[32m'
    beauty=$'\033[35m'
    provenance=$'\033[33m'
    rupture=$'\033[31m'
    ;;
  machtiani-dark)
    truth=$'\033[38;2;88;199;217m'
    goodness=$'\033[38;2;116;201;145m'
    beauty=$'\033[38;2;195;155;232m'
    provenance=$'\033[38;2;215;180;90m'
    rupture=$'\033[38;2;224;108;117m'
    ;;
  machtiani-light)
    truth=$'\033[38;2;0;107;120m'
    goodness=$'\033[38;2;34;107;58m'
    beauty=$'\033[38;2;112;66;143m'
    provenance=$'\033[38;2;121;90;0m'
    rupture=$'\033[38;2;167;46;63m'
    ;;
  none)
    reset=""
    bold=""
    underline=""
    truth=""
    goodness=""
    beauty=""
    provenance=""
    rupture=""
    ;;
  *)
    printf 'unknown theme: %s\n' "$theme" >&2
    usage >&2
    exit 2
    ;;
esac

case "$glyph_mode" in
  unicode)
    outer_char="━"
    command_char="─"
    ;;
  ascii)
    outer_char="="
    command_char="-"
    ;;
  *)
    printf 'unknown glyph mode: %s\n' "$glyph_mode" >&2
    usage >&2
    exit 2
    ;;
esac

case "$outcome" in
  complete|interrupted|input) ;;
  *)
    printf 'unknown outcome: %s\n' "$outcome" >&2
    usage >&2
    exit 2
    ;;
esac

repeat_char() {
  local char="$1"
  local count="$2"
  local value=""
  local i
  for ((i = 0; i < count; i++)); do
    value+="$char"
  done
  printf '%s' "$value"
}

span() {
  local style="$1"
  local value="$2"
  printf '%s%s%s' "$style" "$value" "$reset"
}

terminal_width="${COLUMNS:-}"
if [[ ! "$terminal_width" =~ ^[0-9]+$ ]] || ((terminal_width <= 0)); then
  terminal_width=$(tput cols 2>/dev/null || printf '80')
fi

outer_width=$((terminal_width - 3))
((outer_width > 96)) && outer_width=96
((outer_width < 8)) && outer_width=0

print_outer_rule() {
  if ((outer_width > 0)); then
    printf '  %s\n\n' "$(span "${bold}${beauty}" "$(repeat_char "$outer_char" "$outer_width")")"
  fi
}

print_command_block() {
  local placeholder="$1"
  local session_id='agent-20260718T051605-0813'
  local command="machtiani run --resume $session_id"
  local -a rows
  if [[ -n "$placeholder" ]]; then
    command+=" -p \"$placeholder\""
  fi

  local single="\$ $command"
  if [[ -z "$placeholder" ]] || ((4 + ${#single} + 2 <= terminal_width - 1)); then
    rows=("$single")
  else
    rows=("machtiani run --resume $session_id \\")
    if [[ -n "$placeholder" ]]; then
      rows+=("  -p \"$placeholder\"")
    fi
  fi

  local longest=0
  local row
  for row in "${rows[@]}"; do
    ((${#row} > longest)) && longest=${#row}
  done
  local rule
  rule=$(repeat_char "$command_char" $((longest + 2)))
  printf '    %s\n' "$(span "$goodness" "$rule")"
  if ((${#rows[@]} == 1)); then
    printf '    %s%s%s\n' \
      "$(span "${bold}${goodness}" '$ ')" \
      "$(span "${bold}${provenance}" 'machtiani run')" \
      "${command#machtiani run}"
  else
    printf '    %s %s \\\n' "$(span "${bold}${provenance}" 'machtiani run')" "$session_id"
    if [[ -n "$placeholder" ]]; then
      printf '      -p "%s"\n' "$placeholder"
    fi
  fi
  printf '    %s\n' "$(span "$goodness" "$rule")"
}

print_outer_rule
case "$outcome" in
  complete)
    printf '%s\n' '  Here are the last 5 commit messages:'
    printf '\n'
    printf '%s\n' '  1.  bdd1c6804  feat(session): show final answer path before continuation'
    printf '%s\n' '  2.  f49d1345f  feat(ui): reorganize footer token metrics.'
    printf '%s\n' '  3.  15246b177  feat(ui): show active context window.'
    printf '%s\n' '  4.  443cf9bdc  feat(ui): add active context and cwd to footer'
    printf '%s\n' '  5.  6901c1892  test(discovery): harden native file operations'
    printf '\n\n'
    printf '  %s\n' "$(span "$provenance" 'Answer saved to:')"
    printf '    %s\n\n' "$(span "${underline}${beauty}" '~/.machtiani/7e6be546-9043-42d6-90c9-13cf67c2421f/sessions/agent-20260718T051605-0813/chat/agent-final-answer.md')"
    printf '  %s\n' "$(span "${bold}${goodness}" 'Resume this session:')"
    print_command_block '<your follow-up prompt>'
    ;;
  interrupted)
    printf '  %s\n\n' "$(span "${bold}${rupture}" 'SHELL-AGENT INTERRUPTED')"
    printf '%s\n\n' '  Shell-agent work is resumable.'
    printf '  %s\n' "$(span "${bold}${goodness}" 'Resume the interrupted shell-agent work:')"
    print_command_block ''
    ;;
  input)
    printf '  %s\n\n' "$(span "${bold}${provenance}" 'USER INPUT NEEDED')"
    printf '  %s\n' "$(span "$truth" 'Why this needs your input:')"
    printf '%s\n\n' '    The safer fix preserves existing behavior.'
    printf '  %s\n' "$(span "${bold}${beauty}" 'Your decision:')"
    printf '    %s\n\n' "$(span "${bold}${beauty}" '? Do you want the safer fix?')"
    printf '  %s\n' "$(span "${bold}${goodness}" 'Continue with your answer:')"
    print_command_block '<your answer>'
    ;;
esac
printf '\n'
print_outer_rule

printf '  %s  ~/projects/mct  session token input %s (cache %s)  output %s\n' \
  "$(span "${bold}${truth}" '21s')" \
  "$(span "$provenance" '24,706')" \
  "$(span "$provenance" '88%')" \
  "$(span "$provenance" '1,152')"
printf '  %s %s %s  session %s  planner %s high  shell %s high\n' \
  "$(span "${bold}${beauty}" 'code-forge')" \
  "$(span "${bold}${truth}" 'turn 1')" \
  "$(span "${bold}${truth}" 'running')" \
  "$(span "$provenance" 'agent-20260718T051605-0813')" \
  "$(span "$provenance" 'deepseek:deepseek-v4-flash')" \
  "$(span "$provenance" 'deepseek:deepseek-v4-flash')"
