// Command implementations. Each command opens the workspace, delegates to
// internal/pen, and renders the result for humans or as JSON.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/JaydenCJ/cowpen/internal/pen"
)

// open finds the workspace: --root wins; otherwise discover upward from
// the current directory.
func (c *ctx) open() (*pen.Workspace, error) {
	if c.globals.root != "" {
		if _, err := os.Stat(c.globals.root); err != nil {
			return nil, err
		}
		return pen.Open(c.globals.root)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return pen.Discover(cwd)
}

// openOrInit is like open but, for `cowpen new`, initializes a fresh
// workspace at --root (or the current directory) when none exists yet.
func (c *ctx) openOrInit() (*pen.Workspace, error) {
	w, err := c.open()
	if err == nil {
		return w, nil
	}
	if !errors.Is(err, pen.ErrNoWorkspace) {
		return nil, err
	}
	root := c.globals.root
	if root == "" {
		root, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	return pen.Open(root)
}

func (c *ctx) cmdNew(args []string) int {
	note, args, err := takeNote(args)
	if err != nil || len(args) != 0 {
		return c.usageErr("usage: cowpen new [-m NOTE]")
	}
	w, err := c.openOrInit()
	if err != nil {
		return c.fail(err)
	}
	res, err := w.NewPen(note)
	if err != nil {
		return c.fail(err)
	}
	if c.json() {
		return c.emit(map[string]any{
			"id": res.Pen.ID, "files": res.Pen.Files, "bytes": res.Pen.Bytes,
			"stored_bytes": res.StoredBytes, "deduped": res.Deduped,
		})
	}
	fmt.Fprintf(c.stdout, "opened %s — snapshot of %s (%s stored, %d deduped)\n",
		res.Pen.ID, countNoun(res.Pen.Files, "file"), humanBytes(res.StoredBytes), res.Deduped)
	fmt.Fprintf(c.stdout, "edit freely; `cowpen diff` to review, `cowpen rollback` to undo\n")
	return ExitOK
}

func (c *ctx) cmdStatus(args []string) int {
	verify := false
	for _, a := range args {
		if a == "--verify" {
			verify = true
		} else {
			return c.usageErr("usage: cowpen status [--verify]")
		}
	}
	w, err := c.open()
	if err != nil {
		return c.fail(err)
	}
	top, err := w.Top()
	if err != nil {
		return c.fail(err)
	}
	changes, err := w.Status(top, verify)
	if err != nil {
		return c.fail(err)
	}
	sum := pen.Summarize(changes)
	if c.json() {
		if changes == nil {
			changes = []pen.Change{} // agents get [], never null
		}
		code := c.emit(map[string]any{"pen": top.ID, "changes": changes, "summary": sum})
		if code == ExitOK && sum.Total() > 0 {
			return ExitChanges
		}
		return code
	}
	for _, ch := range changes {
		fmt.Fprintf(c.stdout, "%s %s\n", kindLetter(ch.Kind), ch.Path)
	}
	if sum.Total() == 0 {
		fmt.Fprintf(c.stdout, "clean — tree matches pen %s\n", top.ID)
		return ExitOK
	}
	fmt.Fprintf(c.stdout, "%d changed vs %s (%d added, %d modified, %d deleted",
		sum.Total(), top.ID, sum.Added, sum.Modified, sum.Deleted)
	if sum.Mode > 0 || sum.Type > 0 {
		fmt.Fprintf(c.stdout, ", %d mode, %d type", sum.Mode, sum.Type)
	}
	fmt.Fprintf(c.stdout, ")\n")
	return ExitChanges
}

func (c *ctx) cmdDiff(args []string) int {
	w, err := c.open()
	if err != nil {
		return c.fail(err)
	}
	top, err := w.Top()
	if err != nil {
		return c.fail(err)
	}
	out, err := w.Diff(top, args)
	if err != nil {
		return c.fail(err)
	}
	fmt.Fprint(c.stdout, out)
	if out == "" {
		return ExitOK
	}
	return ExitChanges
}

func (c *ctx) cmdCommit(args []string) int {
	note, args, err := takeNote(args)
	if err != nil || len(args) != 0 {
		return c.usageErr("usage: cowpen commit [-m NOTE]")
	}
	w, err := c.open()
	if err != nil {
		return c.fail(err)
	}
	p, sum, err := w.Commit(note)
	if err != nil {
		return c.fail(err)
	}
	if c.json() {
		return c.emit(map[string]any{"pen": p.ID, "summary": sum})
	}
	fmt.Fprintf(c.stdout, "committed %s — %s kept (%d added, %d modified, %d deleted)\n",
		p.ID, countNoun(sum.Total(), "change"), sum.Added, sum.Modified, sum.Deleted)
	return ExitOK
}

func (c *ctx) cmdRollback(args []string) int {
	to := ""
	resume := false
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--to":
			if i+1 >= len(args) {
				return c.usageErr("--to needs a pen id")
			}
			to = args[i+1]
			i += 2
		case "--resume":
			resume = true
			i++
		default:
			return c.usageErr("usage: cowpen rollback [--to ID | --resume]")
		}
	}
	w, err := c.open()
	if err != nil {
		return c.fail(err)
	}
	var res *pen.RollbackResult
	if resume {
		res, err = w.Resume()
	} else {
		res, err = w.Rollback(to)
	}
	if err != nil {
		return c.fail(err)
	}
	if c.json() {
		skipped := res.Skipped
		if skipped == nil {
			skipped = []string{} // agents get [], never null
		}
		return c.emit(map[string]any{
			"pen": res.Pen.ID, "restored": res.Restored, "removed": res.Removed,
			"closed": res.Closed, "skipped": skipped,
		})
	}
	fmt.Fprintf(c.stdout, "rolled back to %s — %d restored, %d removed, %s closed\n",
		res.Pen.ID, res.Restored, res.Removed, countNoun(res.Closed, "pen"))
	for _, s := range res.Skipped {
		fmt.Fprintf(c.stdout, "note: left non-empty directory %s in place\n", s)
	}
	return ExitOK
}

func (c *ctx) cmdList(args []string) int {
	if len(args) != 0 {
		return c.usageErr("usage: cowpen list")
	}
	w, err := c.open()
	if err != nil {
		return c.fail(err)
	}
	ids, err := w.Stack()
	if err != nil {
		return c.fail(err)
	}
	var pens []*pen.Pen
	for _, id := range ids {
		p, err := w.LoadPen(id)
		if err != nil {
			return c.fail(err)
		}
		pens = append(pens, p)
	}
	if c.json() {
		out := make([]map[string]any, 0, len(pens))
		for _, p := range pens {
			out = append(out, map[string]any{
				"id": p.ID, "note": p.Note, "created": p.Created,
				"files": p.Files, "bytes": p.Bytes,
			})
		}
		return c.emit(out)
	}
	if len(pens) == 0 {
		fmt.Fprintln(c.stdout, "no open pens")
		return ExitOK
	}
	fmt.Fprintf(c.stdout, "%-22s  %-20s  %7s  %9s  %s\n", "ID", "CREATED", "FILES", "SIZE", "NOTE")
	for _, p := range pens {
		fmt.Fprintf(c.stdout, "%-22s  %-20s  %7d  %9s  %s\n",
			p.ID, p.Created.Format("2006-01-02 15:04:05"), p.Files, humanBytes(p.Bytes), p.Note)
	}
	fmt.Fprintf(c.stdout, "%s open; top is %s\n", countNoun(len(pens), "pen"), pens[len(pens)-1].ID)
	return ExitOK
}

func (c *ctx) cmdShow(args []string) int {
	if len(args) != 1 {
		return c.usageErr("usage: cowpen show ID")
	}
	w, err := c.open()
	if err != nil {
		return c.fail(err)
	}
	p, err := w.LoadPen(args[0])
	if err != nil {
		return c.fail(err)
	}
	changes, err := w.Status(p, false)
	if err != nil {
		return c.fail(err)
	}
	sum := pen.Summarize(changes)
	if c.json() {
		return c.emit(map[string]any{
			"id": p.ID, "note": p.Note, "created": p.Created,
			"files": p.Files, "bytes": p.Bytes, "summary": sum,
		})
	}
	fmt.Fprintf(c.stdout, "pen %s\n", p.ID)
	if p.Note != "" {
		fmt.Fprintf(c.stdout, "note:    %s\n", p.Note)
	}
	fmt.Fprintf(c.stdout, "created: %s\n", p.Created.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(c.stdout, "tracks:  %s, %s\n", countNoun(p.Files, "file"), humanBytes(p.Bytes))
	fmt.Fprintf(c.stdout, "changes: %d since snapshot (%d added, %d modified, %d deleted)\n",
		sum.Total(), sum.Added, sum.Modified, sum.Deleted)
	return ExitOK
}

func (c *ctx) cmdLog(args []string) int {
	if len(args) != 0 {
		return c.usageErr("usage: cowpen log")
	}
	w, err := c.open()
	if err != nil {
		return c.fail(err)
	}
	evs, err := w.History()
	if err != nil {
		return c.fail(err)
	}
	if c.json() {
		return c.emit(evs)
	}
	for _, ev := range evs {
		line := fmt.Sprintf("%s  %-11s %s", ev.Time.Format("2006-01-02 15:04:05"), ev.Event, ev.Pen)
		if ev.Note != "" {
			line += fmt.Sprintf("  %q", ev.Note)
		}
		if ev.Event == "committed" {
			line += fmt.Sprintf("  (+%d ~%d -%d)", ev.Added, ev.Modified, ev.Deleted)
		}
		fmt.Fprintln(c.stdout, line)
	}
	return ExitOK
}

func (c *ctx) cmdGC(args []string) int {
	if len(args) != 0 {
		return c.usageErr("usage: cowpen gc")
	}
	w, err := c.open()
	if err != nil {
		return c.fail(err)
	}
	res, err := w.GC()
	if err != nil {
		return c.fail(err)
	}
	if c.json() {
		return c.emit(res)
	}
	fmt.Fprintf(c.stdout, "gc: removed %s, freed %s\n",
		countNoun(res.Removed, "unreferenced object"), humanBytes(res.FreedBytes))
	return ExitOK
}

// takeNote extracts an optional `-m NOTE` from args.
func takeNote(args []string) (string, []string, error) {
	var rest []string
	note := ""
	i := 0
	for i < len(args) {
		if args[i] == "-m" || args[i] == "--message" {
			if i+1 >= len(args) {
				return "", nil, errors.New("-m needs a value")
			}
			note = args[i+1]
			i += 2
			continue
		}
		rest = append(rest, args[i])
		i++
	}
	return note, rest, nil
}

func (c *ctx) emit(v any) int {
	enc := json.NewEncoder(c.stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return c.fail(err)
	}
	return ExitOK
}
