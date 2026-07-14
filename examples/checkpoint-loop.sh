#!/usr/bin/env bash
# checkpoint-loop.sh — stacked pens as per-step checkpoints for a
# multi-step agent task. Each step gets its own pen; a failing step rolls
# back only itself, keeping every earlier step's work. This is the
# pattern coding agents use to make risky migrations resumable.
#
# The demo below runs against a scratch tree so it is safe to execute
# from anywhere; swap the `step_*` functions for real agent invocations.
set -euo pipefail

DEMO="$(mktemp -d)"
trap 'rm -rf "$DEMO"' EXIT
cd "$DEMO"
printf 'v1\n' > config.txt
printf 'core logic\n' > app.txt

step_rename()  { printf 'v2 renamed\n' > config.txt; }
step_migrate() { printf 'migrated\n' >> app.txt; }
step_flaky()   { printf 'half-done garbage\n' > app.txt; return 1; }

run_step() {
  local name="$1"; shift
  cowpen new -m "step: $name" > /dev/null
  if "$@"; then
    cowpen commit -m "step: $name ok" > /dev/null
    echo "kept:       $name"
  else
    cowpen rollback > /dev/null
    echo "rolled back: $name (step failed, earlier steps intact)"
  fi
}

run_step "rename config" step_rename
run_step "migrate app"   step_migrate
run_step "flaky rewrite" step_flaky

echo
echo "final tree state:"
grep -H '' config.txt app.txt
