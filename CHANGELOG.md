# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-13

### Added

- Throwaway copy-on-write pens: `cowpen new` snapshots the tree into a
  content-addressed SHA-256 object store (identical content stored once,
  across pens and paths) and the agent keeps editing in place — no
  containers, no overlayfs, no syscall interception.
- Change detection with a git-style fast path: size+mtime+mode decides
  cheaply, content is hashed only on disagreement, and `--verify`
  re-hashes everything to catch mtime-preserving edits; byte-identical
  rewrites are never reported as changes.
- Reviewable diffs: a built-in Myers O(ND) differ renders git-compatible
  unified hunks with correct `@@` headers and `\ No newline at end of
  file` markers; binary files, symlink retargets, mode flips, and type
  changes get one-line notices; path filters scope the review.
- Atomic, journaled rollback: restores are prepared as temp files next
  to their destinations, a journal is written before any mutation, and
  every apply step is idempotent — `cowpen rollback --resume` finishes
  an interrupted restore; files, modes, mtimes, symlinks, empty dirs and
  deleted directory trees all come back exactly.
- Stacked checkpoints: pens nest; `commit` accepts the top pen's changes
  while outer pens stay armed, `rollback --to <id>` unwinds any depth,
  and unique ID prefixes are accepted everywhere.
- Safety rails: `.cowpenignore` (gitignore-subset) plus built-in
  `.cowpen/` and `.git/` exclusions; rollback refuses to delete added
  directories that still hold untracked files; mutating commands refuse
  to run over a pending journal.
- Operational commands: `status`/`diff` with a scriptable exit-code
  contract (0 clean, 1 changes, 2 usage, 3 runtime), `list`, `show`,
  an append-only `log` of opened/committed/rolled_back events, `gc` for
  unreferenced blobs, and `--format json` for agent integration.
- Runnable examples (`examples/agent-guard.sh`,
  `examples/checkpoint-loop.sh`) and an on-disk format reference
  (`docs/format.md`).
- 89 deterministic offline tests (pinned mtimes, temp-dir workspaces,
  in-process CLI integration) and `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/cowpen/releases/tag/v0.1.0
