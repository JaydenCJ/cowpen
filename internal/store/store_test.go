// Tests for the content-addressed object store: hashing correctness,
// deduplication, immutability, and the gc plumbing (List/Remove/Sweep).
package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutBytesRoundTrip(t *testing.T) {
	s := newStore(t)
	h, err := s.PutBytes([]byte("hello store"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadAll(h)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello store" {
		t.Fatalf("round trip corrupted content: %q", got)
	}
}

func TestHashMatchesKnownSHA256Vector(t *testing.T) {
	s := newStore(t)
	// sha256("abc") — the FIPS 180 test vector.
	h, err := s.PutBytes([]byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	want := "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if h != want {
		t.Fatalf("hash = %s, want %s", h, want)
	}
}

func TestIdenticalContentIsStoredOnce(t *testing.T) {
	s := newStore(t)
	h1, _ := s.PutBytes([]byte("same bytes"))
	h2, _ := s.PutBytes([]byte("same bytes"))
	if h1 != h2 {
		t.Fatalf("same content produced different hashes: %s vs %s", h1, h2)
	}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("dedup failed: %d objects stored", len(all))
	}
}

func TestPutFileStreamsFromDisk(t *testing.T) {
	s := newStore(t)
	src := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(src, []byte("file body"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := s.PutFile(src)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := s.ReadAll(h)
	if string(got) != "file body" {
		t.Fatalf("PutFile round trip: %q", got)
	}
}

func TestObjectsArePublishedReadOnly(t *testing.T) {
	s := newStore(t)
	h, _ := s.PutBytes([]byte("immutable"))
	info, err := os.Stat(s.Path(h))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("object is writable (mode %v); blobs must be immutable", info.Mode())
	}
}

func TestOpenMissingObjectFailsWithShortHash(t *testing.T) {
	s := newStore(t)
	_, err := s.Open("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err == nil {
		t.Fatal("opening a missing object must fail")
	}
	// The error names the object by short hash, not a 64-char wall.
	if want := "deadbeefdead"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error should carry the short hash %q: %v", want, err)
	}
}

func TestListIsSortedAndCompleteAfterManyPuts(t *testing.T) {
	s := newStore(t)
	for _, body := range []string{"one", "two", "three", "four"} {
		if _, err := s.PutBytes([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 objects, got %d", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1] >= all[i] {
			t.Fatalf("List not sorted: %s before %s", all[i-1], all[i])
		}
	}
}

func TestRemoveReportsFreedBytesAndDeletes(t *testing.T) {
	s := newStore(t)
	h, _ := s.PutBytes([]byte("twelve bytes"))
	n, err := s.Remove(h)
	if err != nil {
		t.Fatal(err)
	}
	if n != 12 {
		t.Fatalf("freed %d bytes, want 12", n)
	}
	if s.Has(h) {
		t.Fatal("object still present after Remove")
	}
}

func TestSweepCleansStrayTempFiles(t *testing.T) {
	s := newStore(t)
	// Simulate an interrupted write: a temp file left behind.
	stray := filepath.Join(s.dir, "tmp-interrupted")
	if err := os.WriteFile(stray, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := s.Sweep()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("swept %d temps, want 1", n)
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stray temp survived the sweep")
	}
	// While a stray temp exists it must also be invisible to List.
	s.PutBytes([]byte("real object"))
	os.WriteFile(filepath.Join(s.dir, "tmp-x"), []byte("junk"), 0o644)
	all, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("List must skip temp files, got %d entries", len(all))
	}
}
