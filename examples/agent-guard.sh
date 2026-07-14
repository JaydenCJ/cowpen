#!/usr/bin/env bash
# agent-guard.sh — run any command inside a throwaway pen and decide
# afterwards. Opens a pen, runs the command (your coding agent, a codemod,
# a flaky script), prints the diff, then asks whether to keep or discard
# the changes. Non-interactive mode: pass --auto to keep on exit 0 and
# roll back on failure.
#
# Usage:
#   bash examples/agent-guard.sh [--auto] <command> [args...]
#
# Example:
#   bash examples/agent-guard.sh --auto sed -i 's/foo/bar/g' src/*.go
set -euo pipefail

AUTO=0
if [ "${1:-}" = "--auto" ]; then
  AUTO=1
  shift
fi
[ $# -ge 1 ] || { echo "usage: agent-guard.sh [--auto] <command> [args...]" >&2; exit 2; }

cowpen new -m "guard: $*"

set +e
"$@"
CMD_CODE=$?
set -e

echo
echo "--- changes made by: $* (exit $CMD_CODE) ---"
set +e
cowpen diff
DIRTY=$?
set -e

if [ "$DIRTY" -eq 0 ]; then
  echo "no changes; closing the pen"
  cowpen commit -m "guard: no-op"
  exit "$CMD_CODE"
fi

if [ "$AUTO" -eq 1 ]; then
  if [ "$CMD_CODE" -eq 0 ]; then
    cowpen commit -m "guard: auto-kept ($*)"
  else
    echo "command failed; rolling back"
    cowpen rollback
  fi
  exit "$CMD_CODE"
fi

printf 'keep these changes? [y/N] '
read -r ANSWER
case "$ANSWER" in
  y|Y) cowpen commit -m "guard: approved ($*)" ;;
  *)   cowpen rollback ;;
esac
