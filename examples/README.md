# cowpen examples

Both examples are self-contained bash scripts that assume `cowpen` is on
your `PATH` (`go build -o cowpen ./cmd/cowpen` from the repo root).

| Script | What it shows |
|---|---|
| [`agent-guard.sh`](agent-guard.sh) | Wrap any command in a pen: run it, review the diff, then keep or discard interactively — or `--auto` to keep on success and roll back on failure. |
| [`checkpoint-loop.sh`](checkpoint-loop.sh) | Stacked pens as per-step checkpoints: each step of a multi-step task commits or rolls back independently, so one bad step never destroys the previous steps' work. Runs against its own temp tree, safe to execute as-is. |

Try the checkpoint demo directly:

```bash
bash examples/checkpoint-loop.sh
```

Expected output (the flaky third step fails and is undone; the first two
survive):

```text
kept:       rename config
kept:       migrate app
rolled back: flaky rewrite (step failed, earlier steps intact)

final tree state:
config.txt:v2 renamed
app.txt:core logic
app.txt:migrated
```
