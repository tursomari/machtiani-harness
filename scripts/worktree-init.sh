#!/usr/bin/env bash
set -euo pipefail

# ==============================================================================
# worktree-init.sh — Initialize a secondary git worktree for mct-agent sessions
#
# Usage:
#   scripts/worktree-init.sh [--force] <session-id>
#
# This script prepares a secondary (non-primary) git worktree for running
# mct-agent by copying essential configuration, skills, plans, and a specific
# session from the primary worktree into the current worktree.
#
# Specifically, it copies from the primary worktree (first entry in
# `git worktree list --porcelain`) into the current worktree:
#   - .machtiani/config.toml      (configuration file required by all binaries)
#   - skills/                     (optional skill definitions)
#   - plans/                      (optional task plans)
#   - .machtiani/sessions/<session-id>/  (session data to resume or reference)
#   - .machtiani/artifacts/readme (optional artifact readme)
#
# By default, the script will fail if any destination file or directory
# already exists.  Pass --force to overwrite existing destinations without
# prompting.  Destination parent directories are created automatically.
# Files are NOT staged (no git add).
#
# Exit codes: 0 on success, 1 on error.
#
# ------------------------------------------------------------------------------
# Background: git worktree add
#
# A git worktree lets you check out multiple branches of the same repository
# simultaneously in different directories, each with its own working tree but
# sharing a single .git (object database).  This is useful for running mct-agent
# experiments or evaluations in isolation without affecting your main checkout.
#
# Examples:
#
#   # Create a new worktree at ../hotfix from a new branch "hotfix" based on main:
#   git worktree add -b hotfix ../hotfix main
#
#   # Create a worktree for an existing branch:
#   git worktree add ../feature-x feature-x
#
#   # Create a detached-HEAD worktree at a specific commit:
#   git worktree add --detach ../eval-sandbox abc1234
#
#   # After creating the worktree, initialize it for a session:
#   cd ../hotfix && scripts/worktree-init.sh my-session-id
#
#   # List all worktrees (first line is the primary):
#   git worktree list --porcelain
# ==============================================================================

if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
    cat <<USAGE
worktree-init.sh — Initialize a secondary git worktree for mct-agent sessions

Usage:
  scripts/worktree-init.sh [--force] <session-id>
  scripts/worktree-init.sh --help | -h

This script copies essential configuration, skills, plans, and a specific
session from the primary git worktree into the current worktree.

Options:
  --force    Overwrite existing destination files/directories.
             By default, the script fails if any destination already exists.

Copied items:
  - .machtiani/config.toml
  - skills/ (optional)
  - plans/ (optional)
  - .machtiani/sessions/<session-id>/
  - .machtiani/artifacts/readme (optional)

Git worktree examples:
  git worktree add -b hotfix ../hotfix main
  git worktree add ../feature-x feature-x
  git worktree add --detach ../eval-sandbox abc1234
  cd ../hotfix && scripts/worktree-init.sh my-session-id
USAGE
    exit 0
fi

# --- argument validation ------------------------------------------------------

FORCE=false
if [[ "${1:-}" == "--force" ]]; then
    FORCE=true
    shift
fi

if [ $# -ne 1 ]; then
    echo "Usage: $0 [--force] <session-id>" >&2
    exit 1
fi

SESSION_ID="$1"

if [[ -z "$SESSION_ID" ]]; then
    echo "Error: session-id must not be empty" >&2
    exit 1
fi

if [[ "$SESSION_ID" == *"/"* ]]; then
    echo "Error: session-id must not contain path separators" >&2
    exit 1
fi

# --- worktree detection -------------------------------------------------------

if ! git rev-parse --is-inside-work-tree &>/dev/null; then
    echo "Error: not inside a git worktree (run from a worktree directory)" >&2
    exit 1
fi

PRIMARY=$(git worktree list --porcelain | head -n 1 | sed -n 's/^worktree //p')
if [[ -z "$PRIMARY" ]]; then
    echo "Error: could not determine primary worktree path" >&2
    exit 1
fi

CURRENT=$(git rev-parse --show-toplevel)

if [[ "$CURRENT" == "$PRIMARY" ]]; then
    echo "Warning: already in the primary worktree ($PRIMARY)"
    echo "This script is designed to initialize a secondary worktree."
    echo "Proceeding anyway..."
    echo
fi

# --- accumulate conflicts ------------------------------------------------------

CONFLICTS=()

# --- check config.toml ---------------------------------------------------------

if [ ! -f "$PRIMARY/.machtiani/config.toml" ]; then
    echo "Error: config.toml not found in primary worktree ($PRIMARY/.machtiani/config.toml)" >&2
    exit 1
fi
if [ "$FORCE" != true ] && [ -e "$CURRENT/.machtiani/config.toml" ]; then
    CONFLICTS+=("$CURRENT/.machtiani/config.toml")
fi

# --- check skills --------------------------------------------------------------

if [ -d "$PRIMARY/skills" ]; then
    if [ "$FORCE" != true ] && [ -e "$CURRENT/skills" ]; then
        CONFLICTS+=("$CURRENT/skills")
    fi
fi

# --- check plans ---------------------------------------------------------------

if [ -d "$PRIMARY/plans" ]; then
    if [ "$FORCE" != true ] && [ -e "$CURRENT/plans" ]; then
        CONFLICTS+=("$CURRENT/plans")
    fi
fi

# --- check session -------------------------------------------------------------

SESSION_SRC="$PRIMARY/.machtiani/sessions/$SESSION_ID"
if [ ! -d "$SESSION_SRC" ]; then
    echo "Error: session '$SESSION_ID' not found in primary worktree ($SESSION_SRC)" >&2
    exit 1
fi

SESSION_DST="$CURRENT/.machtiani/sessions/$SESSION_ID"
if [ "$FORCE" != true ] && [ -e "$SESSION_DST" ]; then
    CONFLICTS+=("$SESSION_DST")
fi

# --- check artifacts/readme ----------------------------------------------------

if [ -d "$PRIMARY/.machtiani/artifacts/readme" ]; then
    if [ "$FORCE" != true ] && [ -e "$CURRENT/.machtiani/artifacts/readme" ]; then
        CONFLICTS+=("$CURRENT/.machtiani/artifacts/readme")
    fi
fi

# --- report conflicts ----------------------------------------------------------

if [ "$FORCE" != true ] && [ ${#CONFLICTS[@]} -gt 0 ]; then
    echo "Error: the following files or directories already exist:" >&2
    for item in "${CONFLICTS[@]}"; do
        echo "  - $item" >&2
    done
    echo "Use --force to overwrite." >&2
    exit 1
fi

# --- copy configuration -------------------------------------------------------

echo "==> Copying .machtiani/config.toml ..."
mkdir -p "$CURRENT/.machtiani"
cp "$PRIMARY/.machtiani/config.toml" "$CURRENT/.machtiani/config.toml"
echo "    .machtiani/config.toml copied."

# --- copy skills directory ----------------------------------------------------

echo "==> Copying skills/ directory ..."
if [ -d "$PRIMARY/skills" ]; then
    mkdir -p "$CURRENT/skills"
    cp -r "$PRIMARY/skills/." "$CURRENT/skills/"
    echo "    skills/ copied."
else
    echo "    Warning: skills/ not found in primary worktree, skipping."
fi

# --- copy plans directory -----------------------------------------------------

echo "==> Copying plans/ directory ..."
if [ -d "$PRIMARY/plans" ]; then
    mkdir -p "$CURRENT/plans"
    cp -r "$PRIMARY/plans/." "$CURRENT/plans/"
    echo "    plans/ copied."
else
    echo "    Warning: plans/ not found in primary worktree, skipping."
fi

# --- copy session directory ---------------------------------------------------

echo "==> Copying session '$SESSION_ID' ..."
mkdir -p "$(dirname "$SESSION_DST")"
cp -r "$SESSION_SRC/." "$SESSION_DST/"
echo "    Session '$SESSION_ID' copied."

# --- copy artifacts/readme ----------------------------------------------------

echo "==> Copying .machtiani/artifacts/readme ..."
if [ -d "$PRIMARY/.machtiani/artifacts/readme" ]; then
    mkdir -p "$CURRENT/.machtiani/artifacts"
    cp -r "$PRIMARY/.machtiani/artifacts/readme/." "$CURRENT/.machtiani/artifacts/readme/"
    echo "    .machtiani/artifacts/readme copied."
else
    echo "    Warning: .machtiani/artifacts/readme not found in primary worktree, skipping."
fi

# --- done ---------------------------------------------------------------------

echo
echo "Done. Worktree initialized with session '$SESSION_ID'."

