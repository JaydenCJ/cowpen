// Tests for the tree scanner: ordering, ignore integration, symlink and
// directory handling, and the lazy-vs-eager hashing contract.
package scan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JaydenCJ/cowpen/internal/ignore"
)

func noIgnore(t *testing.T) *ignore.Matcher {
	t.Helper()
	m, err := ignore.New("")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEntriesAreSortedByPath(t *testing.T) {
	root := t.TempDir()
	write(t, root, "zebra.txt", "z")
	write(t, root, "alpha.txt", "a")
	write(t, root, "mid/inner.txt", "m")
	entries, err := Tree(root, noIgnore(t), false)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range entries {
		paths = append(paths, e.Path)
	}
	want := []string{"alpha.txt", "mid", "mid/inner.txt", "zebra.txt"}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths = %v, want %v", paths, want)
		}
	}
}

func TestDirectoriesAreTrackedWithModes(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "restricted")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := Tree(root, noIgnore(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Type != TypeDir {
		t.Fatalf("expected one dir entry, got %+v", entries)
	}
	if entries[0].Mode != 0o700 {
		t.Fatalf("dir mode = %04o, want 0700", entries[0].Mode)
	}
}

func TestSymlinksAreRecordedNotFollowed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "real.txt", "content")
	if err := os.Symlink("real.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	entries, err := Tree(root, noIgnore(t), true)
	if err != nil {
		t.Fatal(err)
	}
	var link *Entry
	for i := range entries {
		if entries[i].Path == "link" {
			link = &entries[i]
		}
	}
	if link == nil || link.Type != TypeSymlink {
		t.Fatalf("symlink not recorded: %+v", entries)
	}
	if link.Target != "real.txt" {
		t.Fatalf("target = %q, want real.txt", link.Target)
	}
	if link.Hash != "" {
		t.Fatal("symlinks must not be content-hashed (never followed)")
	}
	// Agents create dangling links all the time; the scanner must record
	// them rather than erroring on the missing target.
	if err := os.Symlink("does-not-exist", filepath.Join(root, "dangling")); err != nil {
		t.Fatal(err)
	}
	entries, err = Tree(root, noIgnore(t), true)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Path != "dangling" || entries[0].Target != "does-not-exist" {
		t.Fatalf("dangling symlink mishandled: %+v", entries)
	}
}

func TestHashOnDemandOnly(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.txt", "abc")
	lazy, err := Tree(root, noIgnore(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if lazy[0].Hash != "" {
		t.Fatal("hash=false must not compute hashes")
	}
	eager, err := Tree(root, noIgnore(t), true)
	if err != nil {
		t.Fatal(err)
	}
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if eager[0].Hash != want {
		t.Fatalf("hash = %s, want sha256(abc)", eager[0].Hash)
	}
}

func TestSizeAndMtimeAreRecorded(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.txt", "12345")
	entries, err := Tree(root, noIgnore(t), false)
	if err != nil {
		t.Fatal(err)
	}
	e := entries[0]
	if e.Size != 5 {
		t.Fatalf("size = %d, want 5", e.Size)
	}
	if e.MtimeNS == 0 {
		t.Fatal("mtime must be recorded for the fast-path comparison")
	}
}

func TestIgnoredSubtreeIsPruned(t *testing.T) {
	root := t.TempDir()
	write(t, root, "keep.txt", "k")
	write(t, root, "node_modules/dep/index.js", "x")
	m, err := ignore.New("node_modules/")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := Tree(root, m, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "keep.txt" {
		t.Fatalf("ignored subtree leaked into the scan: %+v", entries)
	}
}

func TestByPathIndexesEveryEntry(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "1")
	write(t, root, "b/c.txt", "2")
	entries, err := Tree(root, noIgnore(t), false)
	if err != nil {
		t.Fatal(err)
	}
	idx := ByPath(entries)
	if len(idx) != len(entries) {
		t.Fatalf("index size %d != entries %d", len(idx), len(entries))
	}
	if _, ok := idx["b/c.txt"]; !ok {
		t.Fatal("nested path missing from index")
	}
}

func TestExecutableBitIsCaptured(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "run.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := Tree(root, noIgnore(t), false)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Mode != 0o755 {
		t.Fatalf("mode = %04o, want 0755 (rollback must restore the exec bit)", entries[0].Mode)
	}
}
