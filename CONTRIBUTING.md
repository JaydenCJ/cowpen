# Contributing to cowpen

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22; nothing else — the project has zero runtime
dependencies and the test suite runs fully offline.

```bash
git clone https://github.com/JaydenCJ/cowpen && cd cowpen
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, opens a pen over a throwaway tree
in a temp dir, trashes it like a careless agent, and walks the whole
lifecycle — status, diff, atomic rollback, commit, stacked pens, gc, and
the audit log; it must finish by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (89 deterministic tests, no network).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (the scanner, store, differ, and pen engine never touch the
   terminal — only `cli` does user-facing I/O).

## Ground rules

- Keep dependencies at zero; adding one needs strong justification in
  the PR.
- No network calls ever, no telemetry; cowpen reads and writes files
  under the workspace root and nowhere else.
- Rollback must stay two-phase and journaled: any change that can leave
  the tree half-restored with no way for `rollback --resume` to finish
  is a bug, even if a test doesn't catch it yet.
- Change detection must never silently trust less than size+mtime+mode;
  ambiguity falls through to hashing, not to "unchanged".
- Code comments and doc comments are written in English.

## Reporting bugs

Include the output of `cowpen version`, the exact commands you ran, the
output of `cowpen status --verify` and `cowpen list`, and — for restore
problems — the contents of `.cowpen/journal.json` if present, since that
records exactly which step of the rollback was in flight.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
