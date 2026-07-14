// Tests for the Myers diff and unified rendering. The hunk headers here
// are checked character-for-character because downstream tools (patch,
// review UIs) parse them; an off-by-one line number corrupts a patch.
package difftext

import (
	"strings"
	"testing"
)

func lines(ls ...string) []byte {
	if len(ls) == 0 {
		return nil
	}
	return []byte(strings.Join(ls, "\n") + "\n")
}

func TestIdenticalInputsProduceEmptyDiff(t *testing.T) {
	a := lines("one", "two")
	if got := Unified("a/f", "b/f", a, a, 3); got != "" {
		t.Fatalf("identical inputs must yield an empty diff, got:\n%s", got)
	}
	if got := Unified("a/f", "b/f", nil, nil, 3); got != "" {
		t.Fatalf("empty vs empty must be an empty diff, got %q", got)
	}
}

func TestSingleLineChange(t *testing.T) {
	got := Unified("a/f", "b/f", lines("hello"), lines("goodbye"), 3)
	want := "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-hello\n+goodbye\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestPureAdditionAndPureDeletionAgainstEmptyFiles(t *testing.T) {
	got := Unified("/dev/null", "b/f", nil, lines("new line"), 3)
	want := "--- /dev/null\n+++ b/f\n@@ -0,0 +1 @@\n+new line\n"
	if got != want {
		t.Fatalf("addition:\n%q\nwant:\n%q", got, want)
	}
	got = Unified("a/f", "/dev/null", lines("old line"), nil, 3)
	want = "--- a/f\n+++ /dev/null\n@@ -1 +0,0 @@\n-old line\n"
	if got != want {
		t.Fatalf("deletion:\n%q\nwant:\n%q", got, want)
	}
}

func TestContextSurroundsTheChange(t *testing.T) {
	a := lines("1", "2", "3", "4", "5", "6", "7", "8", "9")
	b := lines("1", "2", "3", "4", "FIVE", "6", "7", "8", "9")
	got := Unified("a/f", "b/f", a, b, 3)
	want := "--- a/f\n+++ b/f\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+FIVE\n 6\n 7\n 8\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	// Zero context is valid too and produces a tight, correct hunk.
	got = Unified("a/f", "b/f", a, b, 0)
	want = "--- a/f\n+++ b/f\n@@ -5 +5 @@\n-5\n+FIVE\n"
	if got != want {
		t.Fatalf("zero context got:\n%q\nwant:\n%q", got, want)
	}
}

func TestDistantChangesGetSeparateHunks(t *testing.T) {
	var av, bv []string
	for i := 0; i < 30; i++ {
		av = append(av, "line")
		bv = append(bv, "line")
	}
	av[2], bv[2] = "a-first", "b-first"
	av[27], bv[27] = "a-last", "b-last"
	got := Unified("a/f", "b/f", lines(av...), lines(bv...), 3)
	if n := strings.Count(got, "@@ -"); n != 2 {
		t.Fatalf("expected 2 hunks, got %d in:\n%s", n, got)
	}
	// The second hunk must carry correct absolute line numbers (only two
	// trailing context lines exist before the end of the file).
	if !strings.Contains(got, "@@ -25,6 +25,6 @@") {
		t.Fatalf("second hunk header wrong:\n%s", got)
	}
}

func TestNearbyChangesMergeIntoOneHunk(t *testing.T) {
	a := lines("1", "2", "3", "4", "5", "6", "7", "8")
	b := lines("1", "TWO", "3", "4", "FIVE", "6", "7", "8")
	got := Unified("a/f", "b/f", a, b, 3)
	if n := strings.Count(got, "@@ -"); n != 1 {
		t.Fatalf("changes 3 lines apart with context 3 must merge, got %d hunks:\n%s", n, got)
	}
}

func TestNoTrailingNewlineMarkersOnBothSides(t *testing.T) {
	// Trailing-newline-only differences are real changes and must carry
	// the "\ No newline" marker on whichever side lacks it.
	got := Unified("a/f", "b/f", []byte("no newline"), lines("no newline"), 3)
	if !strings.Contains(got, "-no newline\n\\ No newline at end of file\n") {
		t.Fatalf("missing marker on the deleted side:\n%s", got)
	}
	got = Unified("a/f", "b/f", lines("x"), []byte("y"), 3)
	if !strings.Contains(got, "+y\n\\ No newline at end of file\n") {
		t.Fatalf("missing marker on the added side:\n%s", got)
	}
}

func TestCommonPrefixAndSuffixAreNotEmittedOutsideContext(t *testing.T) {
	var av, bv []string
	for i := 0; i < 100; i++ {
		av = append(av, "same")
		bv = append(bv, "same")
	}
	bv[50] = "different"
	got := Unified("a/f", "b/f", lines(av...), lines(bv...), 3)
	// 1 change + 6 context + 2 headers + 1 hunk line = 11 lines total.
	if n := strings.Count(got, "\n"); n != 11 {
		t.Fatalf("expected exactly 11 output lines, got %d:\n%s", n, got)
	}
}

func TestInsertionInTheMiddle(t *testing.T) {
	a := lines("alpha", "omega")
	b := lines("alpha", "beta", "omega")
	got := Unified("a/f", "b/f", a, b, 3)
	want := "--- a/f\n+++ b/f\n@@ -1,2 +1,3 @@\n alpha\n+beta\n omega\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestUnicodeLinesSurvive(t *testing.T) {
	got := Unified("a/f", "b/f", lines("こんにちは"), lines("さようなら"), 3)
	if !strings.Contains(got, "-こんにちは\n+さようなら\n") {
		t.Fatalf("multi-byte lines mangled:\n%s", got)
	}
}

func TestMyersFindsMinimalScriptForSwap(t *testing.T) {
	// A classic Myers example: the edit script must not degenerate into
	// delete-everything + insert-everything.
	a := lines("a", "b", "c", "a", "b", "b", "a")
	b := lines("c", "b", "a", "b", "a", "c")
	got := Unified("a/f", "b/f", a, b, 0)
	minus, plus := 0, 0
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "---") || strings.HasPrefix(l, "+++") {
			continue
		}
		if strings.HasPrefix(l, "-") {
			minus++
		}
		if strings.HasPrefix(l, "+") {
			plus++
		}
	}
	if minus+plus > 5 {
		t.Fatalf("expected a minimal script (<=5 edits), got %d-/%d+:\n%s", minus, plus, got)
	}
}

func TestIsBinaryUsesNULHeuristicOnHeadOnly(t *testing.T) {
	if !IsBinary([]byte{'E', 'L', 'F', 0, 1, 2}) {
		t.Error("NUL in the head must classify as binary")
	}
	if IsBinary([]byte("plain text\nwith lines\n")) {
		t.Error("plain text must not classify as binary")
	}
	b := make([]byte, 9000)
	for i := range b {
		b[i] = 'x'
	}
	b[8500] = 0 // past the 8000-byte window
	if IsBinary(b) {
		t.Error("a NUL after the 8000-byte window must not flip the heuristic")
	}
}
