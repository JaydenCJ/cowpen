# cowpen on-disk format (v0.1.0)

Everything cowpen knows lives under `<workspace root>/.cowpen/`. All of
it is plain JSON and content-addressed blobs — inspectable with `ls`,
`cat`, and `jq`, recoverable by hand in the worst case.

```
.cowpen/
├── objects/            # content-addressed blob store
│   └── ab/cdef…        # sha256 fan-out: first 2 hex chars / remaining 62
├── pens/
│   └── <pen-id>.json   # one manifest per open pen
├── stack.json          # open pen IDs, bottom (oldest) → top (newest)
├── history.jsonl       # append-only audit log, one JSON event per line
└── journal.json        # present ONLY while a rollback is applying
```

## Objects

Every snapshotted file body is stored once under
`objects/<h[:2]>/<h[2:]>` where `h` is its SHA-256. Objects are written
via temp file + atomic rename and published mode `0444` (read-only).
Identical content — across paths, pens, and time — occupies one object;
that deduplication is why stacking a pen over an unchanged tree stores
zero new bytes. `cowpen gc` deletes objects no open pen references.

## Pen manifests

A pen is a full manifest of the tree at snapshot time:

```json
{
  "id": "p-djx7opeby6vf-b42a",
  "note": "before agent session",
  "created": "2026-07-13T05:58:41Z",
  "files": 2,
  "bytes": 63,
  "entries": [
    { "path": "src", "type": "dir", "mode": 493 },
    { "path": "src/main.go", "type": "file", "mode": 420,
      "size": 48, "mtime_ns": 1783922310000000000, "hash": "9c56cc…" }
  ]
}
```

Entry types are `file` (mode, size, mtime, hash), `dir` (mode — so empty
directories and permissions survive rollback), and `symlink` (`target`,
never followed). Paths are slash-separated and relative to the root.
Irregular files (sockets, devices, FIFOs) are not tracked.

## The stack

`stack.json` is a JSON array of pen IDs. `new` pushes, `commit` pops the
top, `rollback --to <id>` pops the named pen and everything above it.
Commands accept any unique prefix of an ID.

## The journal

Rollback is two-phase. Phase 1 copies every file body to restore from
the object store into a temp file *next to its destination* (same
directory, so the final rename never crosses filesystems) — a failure
here aborts with the tree untouched. Phase 2 first writes
`journal.json`, then applies the steps in order:

| op | meaning |
|---|---|
| `mkdir` | ensure a directory exists with the recorded mode |
| `remove_file` | delete a file/symlink the pen never saw |
| `rename` | move a prepared temp into place (atomic per file) |
| `symlink` | (re)create a symlink to `target` |
| `chmod` | restore permissions |
| `rmdir` | remove an added directory if it is empty |

Every step is idempotent: `rename` verifies the destination hash when
its temp is already gone, `remove_file`/`rmdir` tolerate "already
deleted". If the process dies mid-apply, `cowpen rollback --resume`
replays the journal to completion; every other mutating command refuses
to run until it does. Added directories that still contain untracked or
ignored files are left in place (reported, not deleted) — rollback never
destroys data outside the snapshot's authority.

## History

`history.jsonl` appends one event per lifecycle action:

```json
{"time":"2026-07-13T05:58:42Z","event":"committed","pen":"p-…","note":"docs approved","modified":1}
```

Events are `opened`, `committed` (with added/modified/deleted counts),
and `rolled_back` (with the restored-file count). A torn final line —
possible if the process is killed mid-append — is skipped on read, never
fatal.

## Stability

The format is versioned with the tool; 0.x may change it. Because every
piece is either a hash-named blob or human-readable JSON, migration or
manual recovery is always possible: the worst-case restore is `cat
.cowpen/pens/<id>.json | jq` plus copying blobs out of `objects/`.
