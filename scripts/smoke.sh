#!/usr/bin/env bash
# End-to-end smoke test for cowpen: builds the binary, opens a pen over a
# fresh temp tree, lets a fake "agent" trash it, reviews the damage with
# status/diff, rolls back atomically, then walks the commit path, stacked
# pens, and gc. No network, idempotent, finishes in seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

BIN="$WORKDIR/cowpen"
TREE="$WORKDIR/project"

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/cowpen) || fail "go build failed"

echo "2. version matches manifest"
OUT="$("$BIN" --version)"
[ "$OUT" = "cowpen 0.1.0" ] || fail "--version mismatch"

echo "3. open a pen over a small project tree"
mkdir -p "$TREE/src"
printf 'package main\n\nfunc main() {\n\tprintln("hello")\n}\n' > "$TREE/src/main.go"
printf '# demo project\n' > "$TREE/README.md"
printf 'secret=1\n' > "$TREE/.env"
printf '.env\n' > "$TREE/.cowpenignore"
OUT="$("$BIN" --root "$TREE" new -m "before agent run")"
echo "$OUT" | grep -q "snapshot of 3 files" \
  || fail "new should track 3 files (.env ignored, .cowpenignore tracked)"

echo "4. clean tree: status exits 0, diff is empty"
OUT="$("$BIN" --root "$TREE" status)"
echo "$OUT" | grep -q "^clean" || fail "fresh pen not clean"
OUT="$("$BIN" --root "$TREE" diff)"
[ -z "$OUT" ] || fail "diff on clean tree should be empty"

echo "5. the agent trashes the tree"
printf 'package main\n\nfunc main() {\n\tpanic("oops")\n}\n' > "$TREE/src/main.go"
rm "$TREE/README.md"
printf 'debug debris\n' > "$TREE/scratch.txt"

echo "6. status lists every change and exits 1"
set +e
"$BIN" --root "$TREE" status > "$WORKDIR/status.out"
CODE=$?
set -e
[ "$CODE" -eq 1 ] || fail "dirty status should exit 1, got $CODE"
grep -q "M src/main.go" "$WORKDIR/status.out" || fail "modified file missing"
grep -q "D README.md" "$WORKDIR/status.out" || fail "deleted file missing"
grep -q "A scratch.txt" "$WORKDIR/status.out" || fail "added file missing"
grep -q "3 changed" "$WORKDIR/status.out" || fail "summary count wrong"

echo "7. diff shows reviewable unified hunks"
set +e
"$BIN" --root "$TREE" diff > "$WORKDIR/diff.out"
CODE=$?
set -e
[ "$CODE" -eq 1 ] || fail "diff with changes should exit 1, got $CODE"
grep -q -- '--- a/src/main.go' "$WORKDIR/diff.out" || fail "diff header missing"
grep -q '+	panic("oops")' "$WORKDIR/diff.out" || fail "added line missing"
grep -q -- '-	println("hello")' "$WORKDIR/diff.out" || fail "removed line missing"

echo "8. ignored files never show up"
grep -q '\.env' "$WORKDIR/status.out" && fail ".env leaked into status"

echo "9. rollback restores everything atomically"
OUT="$("$BIN" --root "$TREE" rollback)"
echo "$OUT" | grep -q "2 restored, 1 removed, 1 pen closed" \
  || fail "rollback summary wrong"
grep -q 'println("hello")' "$TREE/src/main.go" || fail "content not restored"
[ -f "$TREE/README.md" ] || fail "deleted file not restored"
[ ! -f "$TREE/scratch.txt" ] || fail "added debris not removed"
[ -f "$TREE/.env" ] || fail "ignored file must survive rollback"

echo "10. the commit path keeps good changes"
"$BIN" --root "$TREE" new -m "safe refactor" > /dev/null
printf '# demo project\n\nNow documented.\n' > "$TREE/README.md"
OUT="$("$BIN" --root "$TREE" commit -m "docs approved")"
echo "$OUT" | grep -q "1 change kept (0 added, 1 modified, 0 deleted)" \
  || fail "commit summary wrong"
grep -q "Now documented." "$TREE/README.md" || fail "commit lost the edit"

echo "11. stacked pens roll back to the outer checkpoint"
OUT="$("$BIN" --root "$TREE" new -m outer)"
FIRST_ID="$(echo "$OUT" | head -1 | awk '{print $2}')"
[ -n "$FIRST_ID" ] || fail "could not extract the outer pen id"
printf 'edit one\n' >> "$TREE/README.md"
"$BIN" --root "$TREE" new -m inner > /dev/null
printf 'edit two\n' >> "$TREE/README.md"
OUT="$("$BIN" --root "$TREE" list)"
echo "$OUT" | grep -q "2 pens open" || fail "stack should hold 2 pens"
"$BIN" --root "$TREE" rollback --to "$FIRST_ID" > /dev/null || fail "rollback --to failed"
grep -q "edit one" "$TREE/README.md" && fail "outer rollback left edit one behind"
OUT="$("$BIN" --root "$TREE" list)"
echo "$OUT" | grep -q "no open pens" || fail "stack should be empty"

echo "12. json output parses and gc reclaims closed pens"
"$BIN" --root "$TREE" new > /dev/null
set +e
OUT="$("$BIN" --root "$TREE" --format json status)"
set -e
echo "$OUT" | grep -q '"summary"' || fail "json status wrong"
"$BIN" --root "$TREE" rollback > /dev/null
OUT="$("$BIN" --root "$TREE" gc)"
echo "$OUT" | grep -Eq "removed [0-9]+ unreferenced object" || fail "gc output wrong"

echo "13. the audit log tells the whole story"
LOG="$("$BIN" --root "$TREE" log)"
for needle in opened committed rolled_back "docs approved"; do
  echo "$LOG" | grep -q "$needle" || fail "log missing: $needle"
done

echo "14. usage errors exit 2"
set +e
"$BIN" --root "$TREE" stampede >/dev/null 2>&1
[ $? -eq 2 ] || fail "unknown command should exit 2"
set -e

echo "SMOKE OK"
