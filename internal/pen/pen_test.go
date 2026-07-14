// Tests for the pen engine: snapshot bookkeeping, change detection with
// the size+mtime fast path, commit semantics, and gc. Rollback has its own
// file. All trees live in t.TempDir(); mtimes are pinned with os.Chtimes
// so nothing depends on filesystem timestamp granularity.
package pen

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// epoch is an arbitrary fixed timestamp; tests move mtimes relative to it
// so "the mtime changed" is always deterministic.
var epoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func newWorkspace(t *testing.T) *Workspace {
	t.Helper()
	w, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func write(t *testing.T, w *Workspace, rel, body string) string {
	t.Helper()
	p := filepath.Join(w.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, epoch, epoch); err != nil {
		t.Fatal(err)
	}
	return p
}

// laterTicks makes every writeLater call use a strictly increasing mtime,
// so consecutive same-size edits are always distinguishable.
var laterTicks time.Duration

// writeLater rewrites a file with a strictly newer mtime, defeating the
// fast path so content comparison decides.
func writeLater(t *testing.T, w *Workspace, rel, body string) {
	t.Helper()
	p := write(t, w, rel, body)
	laterTicks += time.Hour
	ts := epoch.Add(laterTicks)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func mustPen(t *testing.T, w *Workspace, note string) *Pen {
	t.Helper()
	res, err := w.NewPen(note)
	if err != nil {
		t.Fatal(err)
	}
	return res.Pen
}

func mustStatus(t *testing.T, w *Workspace, p *Pen) []Change {
	t.Helper()
	changes, err := w.Status(p, false)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func TestNewPenCountsFilesAndBytes(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "12345")
	write(t, w, "sub/b.txt", "678")
	res, err := w.NewPen("first")
	if err != nil {
		t.Fatal(err)
	}
	if res.Pen.Files != 2 || res.Pen.Bytes != 8 {
		t.Fatalf("files=%d bytes=%d, want 2/8", res.Pen.Files, res.Pen.Bytes)
	}
	if res.StoredBytes != 8 || res.Deduped != 0 {
		t.Fatalf("stored=%d deduped=%d, want 8/0", res.StoredBytes, res.Deduped)
	}
	if changes := mustStatus(t, w, res.Pen); len(changes) != 0 {
		t.Fatalf("fresh pen must be clean, got %+v", changes)
	}
}

func TestSecondPenDedupsUnchangedContent(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "stable content")
	mustPen(t, w, "")
	res, err := w.NewPen("")
	if err != nil {
		t.Fatal(err)
	}
	if res.Deduped != 1 || res.StoredBytes != 0 {
		t.Fatalf("stacked pen over unchanged tree must store 0 new bytes, got stored=%d deduped=%d",
			res.StoredBytes, res.Deduped)
	}
}

func TestStatusDetectsContentChanges(t *testing.T) {
	// Size change hits the fast path; a same-size edit must fall through
	// to hashing (mtime moved, size identical).
	w := newWorkspace(t)
	write(t, w, "a.txt", "short")
	p := mustPen(t, w, "")
	writeLater(t, w, "a.txt", "much longer content")
	changes := mustStatus(t, w, p)
	if len(changes) != 1 || changes[0].Kind != KindModified {
		t.Fatalf("size change: want one modified, got %+v", changes)
	}

	w2 := newWorkspace(t)
	write(t, w2, "a.txt", "aaaa")
	p2 := mustPen(t, w2, "")
	writeLater(t, w2, "a.txt", "bbbb")
	changes = mustStatus(t, w2, p2)
	if len(changes) != 1 || changes[0].Kind != KindModified {
		t.Fatalf("same-size edit must be detected via hashing, got %+v", changes)
	}
}

func TestRewriteWithIdenticalBytesIsNotAChange(t *testing.T) {
	// Formatters often rewrite files without changing a byte. The mtime
	// moves, the hash matches — status must stay clean.
	w := newWorkspace(t)
	write(t, w, "a.txt", "same")
	p := mustPen(t, w, "")
	writeLater(t, w, "a.txt", "same")
	if changes := mustStatus(t, w, p); len(changes) != 0 {
		t.Fatalf("byte-identical rewrite reported as change: %+v", changes)
	}
}

func TestStatusDetectsAddedAndDeleted(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "keep.txt", "k")
	write(t, w, "doomed.txt", "d")
	p := mustPen(t, w, "")
	write(t, w, "fresh.txt", "f")
	if err := os.Remove(filepath.Join(w.Root, "doomed.txt")); err != nil {
		t.Fatal(err)
	}
	changes := mustStatus(t, w, p)
	if len(changes) != 2 {
		t.Fatalf("want 2 changes, got %+v", changes)
	}
	// Sorted by path: doomed.txt (deleted) before fresh.txt (added).
	if changes[0].Kind != KindDeleted || changes[0].Path != "doomed.txt" {
		t.Fatalf("changes[0] = %+v", changes[0])
	}
	if changes[1].Kind != KindAdded || changes[1].Path != "fresh.txt" {
		t.Fatalf("changes[1] = %+v", changes[1])
	}
}

func TestStatusDetectsModeOnlyChange(t *testing.T) {
	w := newWorkspace(t)
	p0 := write(t, w, "run.sh", "#!/bin/sh\n")
	p := mustPen(t, w, "")
	if err := os.Chmod(p0, 0o755); err != nil {
		t.Fatal(err)
	}
	changes := mustStatus(t, w, p)
	if len(changes) != 1 || changes[0].Kind != KindMode {
		t.Fatalf("want one mode change, got %+v", changes)
	}
}

func TestStatusDetectsTypeChangeAndSymlinkRetarget(t *testing.T) {
	w := newWorkspace(t)
	abs := write(t, w, "thing", "was a file")
	link := filepath.Join(w.Root, "current")
	if err := os.Symlink("thing", link); err != nil {
		t.Fatal(err)
	}
	p := mustPen(t, w, "")
	// thing: file → symlink is a type change.
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("elsewhere", abs); err != nil {
		t.Fatal(err)
	}
	// current: retargeting an existing symlink is a modification.
	os.Remove(link)
	if err := os.Symlink("other", link); err != nil {
		t.Fatal(err)
	}
	changes := mustStatus(t, w, p)
	if len(changes) != 2 {
		t.Fatalf("want 2 changes, got %+v", changes)
	}
	if changes[0].Path != "current" || changes[0].Kind != KindModified {
		t.Fatalf("retargeted symlink must be modified, got %+v", changes[0])
	}
	if changes[1].Path != "thing" || changes[1].Kind != KindType {
		t.Fatalf("file→symlink must be a type change, got %+v", changes[1])
	}
}

func TestVerifyCatchesMtimePreservingEdit(t *testing.T) {
	// An adversarially quiet edit: same size, same mtime. The fast path
	// cannot see it; --verify must.
	w := newWorkspace(t)
	abs := write(t, w, "a.txt", "AAAA")
	p := mustPen(t, w, "")
	if err := os.WriteFile(abs, []byte("BBBB"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(abs, epoch, epoch); err != nil {
		t.Fatal(err)
	}
	if changes := mustStatus(t, w, p); len(changes) != 0 {
		t.Fatalf("fast path should trust matching metadata, got %+v", changes)
	}
	verified, err := w.Status(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 1 || verified[0].Kind != KindModified {
		t.Fatalf("verify must hash and catch the edit, got %+v", verified)
	}
}

func TestIgnoredFilesAreInvisibleToStatus(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, ".cowpenignore", "*.log\n")
	w2, err := Open(w.Root) // reload so the ignore file is picked up
	if err != nil {
		t.Fatal(err)
	}
	write(t, w2, "code.go", "package x")
	p := mustPen(t, w2, "")
	write(t, w2, "debug.log", "noise")
	if changes := mustStatus(t, w2, p); len(changes) != 0 {
		t.Fatalf("ignored file leaked into status: %+v", changes)
	}
}

func TestDiffRendersUnifiedHunks(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "main.go", "package main\n\nfunc main() {}\n")
	p := mustPen(t, w, "")
	writeLater(t, w, "main.go", "package main\n\nfunc main() { run() }\n")
	out, err := w.Diff(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- a/main.go", "+++ b/main.go", "-func main() {}", "+func main() { run() }"} {
		if !containsLine(out, want) {
			t.Fatalf("diff missing %q:\n%s", want, out)
		}
	}
}

func TestDiffBinaryAndModeChangesGetOneLineNotices(t *testing.T) {
	w := newWorkspace(t)
	abs := write(t, w, "blob.bin", "text at first")
	sh := write(t, w, "run.sh", "#!/bin/sh\n")
	p := mustPen(t, w, "")
	if err := os.WriteFile(abs, []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sh, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := w.Diff(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "Binary files a/blob.bin and b/blob.bin differ\nmode change 0644 -> 0755: run.sh\n"
	if out != want {
		t.Fatalf("notices wrong:\n%q\nwant:\n%q", out, want)
	}
}

func TestDiffPathFilterSelectsSubtree(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "src/a.go", "a\n")
	write(t, w, "docs/b.md", "b\n")
	p := mustPen(t, w, "")
	writeLater(t, w, "src/a.go", "a changed\n")
	writeLater(t, w, "docs/b.md", "b changed\n")
	out, err := w.Diff(p, []string{"src"})
	if err != nil {
		t.Fatal(err)
	}
	if !containsLine(out, "+++ b/src/a.go") || containsLine(out, "+++ b/docs/b.md") {
		t.Fatalf("path filter leaked or dropped files:\n%s", out)
	}
}

func TestCommitClosesTopPenAndKeepsTree(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "v1")
	mustPen(t, w, "")
	writeLater(t, w, "a.txt", "v2 bigger")
	p, sum, err := w.Commit("looks good")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Modified != 1 {
		t.Fatalf("summary = %+v, want 1 modified", sum)
	}
	ids, _ := w.Stack()
	if len(ids) != 0 {
		t.Fatalf("stack should be empty after commit, got %v", ids)
	}
	if _, err := os.Stat(w.penPath(p.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("committed pen manifest should be deleted")
	}
	body, _ := os.ReadFile(filepath.Join(w.Root, "a.txt"))
	if string(body) != "v2 bigger" {
		t.Fatalf("commit must keep the edited tree, got %q", body)
	}
}

func TestCommitOnlyPopsTheTopOfTheStack(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "base")
	outer := mustPen(t, w, "outer")
	writeLater(t, w, "a.txt", "midway edit")
	mustPen(t, w, "inner")
	if _, _, err := w.Commit(""); err != nil {
		t.Fatal(err)
	}
	ids, _ := w.Stack()
	if len(ids) != 1 || ids[0] != outer.ID {
		t.Fatalf("outer pen must survive an inner commit, stack=%v", ids)
	}
}

func TestLoadPenAcceptsUniquePrefixAndRoundTripsJSON(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "x")
	p := mustPen(t, w, "note with \"quotes\" and 日本語")
	got, err := w.LoadPen(p.ID[:8])
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != p.ID {
		t.Fatalf("prefix lookup returned %s, want %s", got.ID, p.ID)
	}
	if got.Note != p.Note || len(got.Entries) != len(p.Entries) {
		t.Fatalf("manifest round trip lost data: %+v", got)
	}
	if _, err := w.LoadPen("p-nonexistent"); err == nil {
		t.Fatal("unknown prefix must fail")
	}
	// The manifest on disk is valid, indented JSON (greppable by users).
	raw, _ := os.ReadFile(w.penPath(p.ID))
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
}

func TestGCKeepsReferencedRemovesOrphans(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "referenced content")
	mustPen(t, w, "")
	orphan, err := w.store.PutBytes([]byte("orphaned content"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.GC()
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 1 {
		t.Fatalf("gc removed %d, want 1", res.Removed)
	}
	if w.store.Has(orphan) {
		t.Fatal("orphan blob survived gc")
	}
	changes := mustStatus(t, w, mustTop(t, w))
	if len(changes) != 0 {
		t.Fatalf("gc must not disturb the open pen: %+v", changes)
	}
}

func TestHistoryRecordsLifecycleEvents(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "v1")
	mustPen(t, w, "start")
	writeLater(t, w, "a.txt", "v2 longer")
	if _, _, err := w.Commit("done"); err != nil {
		t.Fatal(err)
	}
	evs, err := w.History()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Event != "opened" || evs[1].Event != "committed" {
		t.Fatalf("history = %+v", evs)
	}
	if evs[1].Modified != 1 {
		t.Fatalf("committed event must carry counts: %+v", evs[1])
	}
}

func TestDiscoverWalksUpAndFailsCleanlyOutside(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "deep/nested/f.txt", "x")
	found, err := Discover(filepath.Join(w.Root, "deep", "nested"))
	if err != nil {
		t.Fatal(err)
	}
	if found.Root != w.Root {
		t.Fatalf("Discover found %s, want %s", found.Root, w.Root)
	}
	if _, err := Discover(t.TempDir()); !errors.Is(err, ErrNoWorkspace) {
		t.Fatalf("want ErrNoWorkspace, got %v", err)
	}
}

func TestMutationsRefuseWhilstJournalPending(t *testing.T) {
	w := newWorkspace(t)
	write(t, w, "a.txt", "x")
	mustPen(t, w, "")
	// Simulate a crash that left a rollback journal behind.
	if err := os.WriteFile(w.journalPath(), []byte(`{"pen":"p-x","steps":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.NewPen(""); !errors.Is(err, ErrJournal) {
		t.Fatalf("NewPen: want ErrJournal, got %v", err)
	}
	if _, _, err := w.Commit(""); !errors.Is(err, ErrJournal) {
		t.Fatalf("Commit: want ErrJournal, got %v", err)
	}
	if _, err := w.GC(); !errors.Is(err, ErrJournal) {
		t.Fatalf("GC: want ErrJournal, got %v", err)
	}
	if _, err := w.Rollback(""); !errors.Is(err, ErrJournal) {
		t.Fatalf("Rollback: want ErrJournal, got %v", err)
	}
}

func mustTop(t *testing.T, w *Workspace) *Pen {
	t.Helper()
	p, err := w.Top()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func containsLine(s, sub string) bool { return strings.Contains(s, sub) }
