// Tests for atomic rollback: every restore shape (content, deletions,
// additions, modes, symlinks, directories, type flips), stacked pens, and
// the crash-recovery journal.
package pen

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, w *Workspace, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.Root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRollbackRestoresModifiedContent(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "original")
	p := mustPen(t, w, "")
	writeLater(t, w, "a.txt", "agent scribbles")
	res, err := w.Rollback("")
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 1 || res.Closed != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got := readFile(t, w, "a.txt"); got != "original" {
		t.Fatalf("content = %q, want original", got)
	}
	if p.ID != res.Pen.ID {
		t.Fatalf("rolled back to %s, want %s", res.Pen.ID, p.ID)
	}
	// The two-phase restore must clean up all of its staging temps.
	err = filepath.WalkDir(w.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".cowpen-restore") {
			t.Errorf("leftover restore temp: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRollbackDeletesAddedFilesAndDirs(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "keep.txt", "k")
	mustPen(t, w, "")
	write(t, w, "junk/deep/new.txt", "temp output")
	if _, err := w.Rollback(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.Root, "junk")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("added directory tree must be removed")
	}
	if got := readFile(t, w, "keep.txt"); got != "k" {
		t.Fatal("untouched file must survive rollback")
	}
}

func TestRollbackRecreatesDeletedFileWithItsDirectory(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "pkg/util/helper.go", "package util\n")
	mustPen(t, w, "")
	if err := os.RemoveAll(filepath.Join(w.Root, "pkg")); err != nil {
		t.Fatal(err)
	}
	res, err := w.Rollback("")
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, w, "pkg/util/helper.go"); got != "package util\n" {
		t.Fatalf("deleted file not restored: %q", got)
	}
	if res.Restored != 1 {
		t.Fatalf("restored = %d, want 1", res.Restored)
	}
}

func TestRollbackRestoresModes(t *testing.T) {
	// run.sh: mode-only change. tool.sh: content AND mode changed — the
	// restored file must carry the snapshot's mode, not the temp default.
	w := newWorkspace(t)
	runAbs := write(t, w, "run.sh", "#!/bin/sh\n")
	toolAbs := write(t, w, "tool.sh", "#!/bin/sh\necho v1\n")
	for _, p := range []string{runAbs, toolAbs} {
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustPen(t, w, "")
	if err := os.Chmod(runAbs, 0o600); err != nil {
		t.Fatal(err)
	}
	writeLater(t, w, "tool.sh", "#!/bin/sh\necho v2 -- corrupted\n")
	if _, err := w.Rollback(""); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{runAbs, toolAbs} {
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode = %v, want 0755", p, info.Mode().Perm())
		}
	}
	if got := readFile(t, w, "tool.sh"); got != "#!/bin/sh\necho v1\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestRollbackRestoresSymlinkTarget(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "v1.txt", "one")
	link := filepath.Join(w.Root, "current")
	if err := os.Symlink("v1.txt", link); err != nil {
		t.Fatal(err)
	}
	mustPen(t, w, "")
	os.Remove(link)
	if err := os.Symlink("v2.txt", link); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Rollback(""); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if target != "v1.txt" {
		t.Fatalf("symlink target = %q, want v1.txt", target)
	}
}

func TestRollbackUndoesTypeChange(t *testing.T) {
	w := newWorkspace(t)
	abs := write(t, w, "config", "real file content")
	mustPen(t, w, "")
	os.Remove(abs)
	if err := os.Symlink("/dev/null", abs); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Rollback(""); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("expected a regular file back, got %v", info.Mode())
	}
	if got := readFile(t, w, "config"); got != "real file content" {
		t.Fatalf("content = %q", got)
	}
}

func TestRollbackRestoresEmptyDirectory(t *testing.T) {
	w := newWorkspace(t)
	if err := os.Mkdir(filepath.Join(w.Root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustPen(t, w, "")
	if err := os.Remove(filepath.Join(w.Root, "empty")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Rollback(""); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(w.Root, "empty"))
	if err != nil || !info.IsDir() {
		t.Fatalf("empty tracked directory not restored: %v", err)
	}
}

func TestRollbackToOuterPenClosesTheWholeStack(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "v1")
	outer := mustPen(t, w, "outer")
	writeLater(t, w, "a.txt", "v2 edit one")
	mustPen(t, w, "inner")
	writeLater(t, w, "a.txt", "v3 edit two")
	res, err := w.Rollback(outer.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, w, "a.txt"); got != "v1" {
		t.Fatalf("content = %q, want the outer snapshot v1", got)
	}
	if res.Closed != 2 {
		t.Fatalf("closed = %d, want both pens", res.Closed)
	}
	ids, _ := w.Stack()
	if len(ids) != 0 {
		t.Fatalf("stack should be empty, got %v", ids)
	}
}

func TestRollbackTopPenPreservesOuterCheckpoint(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "v1")
	outer := mustPen(t, w, "outer")
	writeLater(t, w, "a.txt", "v2 kept edit")
	mustPen(t, w, "inner")
	writeLater(t, w, "a.txt", "v3 bad edit!")
	if _, err := w.Rollback(""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, w, "a.txt"); got != "v2 kept edit" {
		t.Fatalf("content = %q, want the inner snapshot v2", got)
	}
	ids, _ := w.Stack()
	if len(ids) != 1 || ids[0] != outer.ID {
		t.Fatalf("outer pen must remain open, stack=%v", ids)
	}
}

func TestRollbackOnCleanTreeIsANoopThatClosesThePen(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "steady")
	mustPen(t, w, "")
	res, err := w.Rollback("")
	if err != nil {
		t.Fatal(err)
	}
	if res.Restored != 0 || res.Removed != 0 || res.Closed != 1 {
		t.Fatalf("result = %+v", res)
	}
	if w.HasJournal() {
		t.Fatal("a no-op rollback must not leave a journal")
	}
}

func TestRollbackLeavesNonEmptyAddedDirInPlace(t *testing.T) {
	// The added dir contains an ignored file the pen never saw. Deleting
	// it would destroy data outside the snapshot's authority.
	w := newWorkspace(t)
	write(t, w, ".cowpenignore", "*.secret\n")
	w2, err := Open(w.Root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, w2, "code.txt", "x")
	mustPen(t, w2, "")
	write(t, w2, "newdir/visible.txt", "tracked")
	write(t, w2, "newdir/keys.secret", "ignored data")
	res, err := w2.Rollback("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w2.Root, "newdir", "keys.secret")); err != nil {
		t.Fatal("ignored file inside added dir must survive")
	}
	if _, err := os.Stat(filepath.Join(w2.Root, "newdir", "visible.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tracked added file must still be removed")
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "newdir" {
		t.Fatalf("skipped = %v, want [newdir]", res.Skipped)
	}
}

func TestResumeFinishesAJournaledRollback(t *testing.T) {
	// Simulate a crash after the journal was written but before any step
	// ran: plan by hand, persist the journal, then Resume.
	w := newWorkspace(t)
	write(t, w, "a.txt", "safe state")
	p := mustPen(t, w, "")
	writeLater(t, w, "a.txt", "crashed mid-edit")
	changes, err := w.Status(p, false)
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := w.Stack()
	j := &journal{Pen: p.ID, ClosePens: ids}
	if err := w.plan(j, p, changes); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(j)
	if err := os.WriteFile(w.journalPath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := w.Resume()
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, w, "a.txt"); got != "safe state" {
		t.Fatalf("content = %q, want the snapshot", got)
	}
	if res.Restored != 1 || w.HasJournal() {
		t.Fatalf("resume incomplete: %+v journal=%v", res, w.HasJournal())
	}
	if s, _ := w.Stack(); len(s) != 0 {
		t.Fatalf("stack should be empty after resume, got %v", s)
	}
}

func TestResumeIsIdempotentAfterPartialApply(t *testing.T) {
	// Crash *during* apply: one rename already happened, its temp is gone.
	// Resume must verify the destination hash and carry on, not fail.
	w := newWorkspace(t)
	write(t, w, "a.txt", "alpha original")
	write(t, w, "b.txt", "beta original")
	p := mustPen(t, w, "")
	writeLater(t, w, "a.txt", "alpha trashed!")
	writeLater(t, w, "b.txt", "beta trashed!!")
	changes, err := w.Status(p, false)
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := w.Stack()
	j := &journal{Pen: p.ID, ClosePens: ids}
	if err := w.plan(j, p, changes); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(j)
	if err := os.WriteFile(w.journalPath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	// Manually execute the first rename step, simulating the crash point.
	var first *Step
	for i := range j.Steps {
		if j.Steps[i].Op == "rename" {
			first = &j.Steps[i]
			break
		}
	}
	if first == nil {
		t.Fatal("plan produced no rename steps")
	}
	if err := os.Rename(
		filepath.Join(w.Root, filepath.FromSlash(first.Tmp)),
		filepath.Join(w.Root, filepath.FromSlash(first.Path)),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Resume(); err != nil {
		t.Fatal(err)
	}
	if readFile(t, w, "a.txt") != "alpha original" || readFile(t, w, "b.txt") != "beta original" {
		t.Fatal("resume did not finish restoring both files")
	}
	if w.HasJournal() {
		t.Fatal("journal must be cleared after resume")
	}
}

func TestRollbackRestoresMtimeForOuterPensFastPath(t *testing.T) {
	// After an inner rollback, a still-open outer pen must see the file
	// as unchanged via metadata alone — no false positives, no hashing.
	w := newWorkspace(t)
	write(t, w, "a.txt", "steady state")
	outer := mustPen(t, w, "outer")
	mustPen(t, w, "inner")
	writeLater(t, w, "a.txt", "inner mess!!")
	if _, err := w.Rollback(""); err != nil {
		t.Fatal(err)
	}
	changes, err := w.Status(outer, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 0 {
		t.Fatalf("outer pen must see a clean tree after inner rollback: %+v", changes)
	}
	info, _ := os.Stat(filepath.Join(w.Root, "a.txt"))
	if info.ModTime().UnixNano() != epoch.UnixNano() {
		t.Fatalf("mtime = %v, want the snapshot's %v", info.ModTime(), epoch)
	}
}

func TestRollbackAndResumePreconditionsFailCleanly(t *testing.T) {
	w := newWorkspace(t)
	if _, err := w.Rollback(""); !errors.Is(err, ErrNoPen) {
		t.Fatalf("rollback with no pen: want ErrNoPen, got %v", err)
	}
	if _, err := w.Resume(); err == nil {
		t.Fatal("resume without a journal must fail with a clear message")
	}
}
