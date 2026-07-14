// In-process CLI integration tests: every command runs through Run() with
// captured writers and a --root pointed at a temp workspace, asserting on
// the exact exit-code contract agents script against.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/JaydenCJ/cowpen/internal/version"
)

// run executes one cowpen invocation against root.
func run(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errB bytes.Buffer
	code := Run(append([]string{"--root", root}, args...), &out, &errB)
	return code, out.String(), errB.String()
}

func writeTree(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// bump gives a file a fresh, strictly-later mtime so same-size edits are
// always visible to the fast path.
func bump(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	ts := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func TestVersionFlagAndCommandAgree(t *testing.T) {
	var out bytes.Buffer
	if code := Run([]string{"--version"}, &out, &out); code != ExitOK {
		t.Fatalf("--version exit = %d", code)
	}
	want := "cowpen " + version.Version + "\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	out.Reset()
	Run([]string{"version"}, &out, &out)
	if out.String() != want {
		t.Fatalf("version command got %q, want %q", out.String(), want)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	var out, errB bytes.Buffer
	if code := Run([]string{"stampede"}, &out, &errB); code != ExitUsage {
		t.Fatalf("unknown command: exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errB.String(), "unknown command") {
		t.Fatalf("stderr: %q", errB.String())
	}
	out.Reset()
	if code := Run(nil, &out, &out); code != ExitUsage {
		t.Fatalf("no args: exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(out.String(), "Usage: cowpen") {
		t.Fatal("usage text missing")
	}
	root := t.TempDir()
	if code, _, _ := run(t, root, "new", "-m"); code != ExitUsage {
		t.Fatalf("dangling -m: exit=%d, want %d", code, ExitUsage)
	}
	out.Reset()
	if code := Run([]string{"--format", "yaml", "list"}, &out, &out); code != ExitUsage {
		t.Fatalf("--format yaml: exit=%d, want %d", code, ExitUsage)
	}
}

func TestHelpExitsZero(t *testing.T) {
	var out bytes.Buffer
	if code := Run([]string{"--help"}, &out, &out); code != ExitOK {
		t.Fatalf("--help exit = %d, want 0", code)
	}
}

func TestNewOpensAPenAndPrintsID(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "hello")
	code, out, _ := run(t, root, "new", "-m", "before agent")
	if code != ExitOK {
		t.Fatalf("exit = %d, out=%s", code, out)
	}
	if !regexp.MustCompile(`opened p-[a-z0-9]+-[0-9a-f]{4} — snapshot of 1 file \(`).MatchString(out) {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestStatusExitCodesFollowCleanliness(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "hello")
	run(t, root, "new")
	code, out, _ := run(t, root, "status")
	if code != ExitOK || !strings.Contains(out, "clean") {
		t.Fatalf("clean tree: exit=%d out=%q", code, out)
	}
	writeTree(t, root, "b.txt", "new file")
	code, out, _ = run(t, root, "status")
	if code != ExitChanges {
		t.Fatalf("dirty tree: exit=%d, want %d", code, ExitChanges)
	}
	if !strings.Contains(out, "A b.txt") {
		t.Fatalf("status lines: %q", out)
	}
}

func TestDiffPrintsHunksAndExitsOne(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "main.txt", "line one\nline two\n")
	run(t, root, "new")
	code0, out0, _ := run(t, root, "diff")
	if code0 != ExitOK || out0 != "" {
		t.Fatalf("clean diff: exit=%d out=%q", code0, out0)
	}
	writeTree(t, root, "main.txt", "line one\nline 2!\n")
	bump(t, root, "main.txt")
	code, out, _ := run(t, root, "diff")
	if code != ExitChanges {
		t.Fatalf("exit = %d, want %d", code, ExitChanges)
	}
	for _, want := range []string{"--- a/main.txt", "+++ b/main.txt", "-line two", "+line 2!"} {
		if !strings.Contains(out, want) {
			t.Fatalf("diff missing %q:\n%s", want, out)
		}
	}
}

func TestCommitKeepsChangesAndClosesPen(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "v1")
	run(t, root, "new")
	writeTree(t, root, "a.txt", "v2 longer")
	code, out, _ := run(t, root, "commit", "-m", "approved")
	if code != ExitOK || !strings.Contains(out, "1 modified") {
		t.Fatalf("commit: exit=%d out=%q", code, out)
	}
	body, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(body) != "v2 longer" {
		t.Fatalf("tree lost the committed edit: %q", body)
	}
	code, _, _ = run(t, root, "status")
	if code != ExitRuntime {
		t.Fatalf("status with no pen should be a runtime error, got %d", code)
	}
}

func TestRollbackRestoresViaCLI(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "precious")
	run(t, root, "new")
	writeTree(t, root, "a.txt", "agent broke this file entirely")
	writeTree(t, root, "junk.txt", "debris")
	code, out, _ := run(t, root, "rollback")
	if code != ExitOK {
		t.Fatalf("rollback exit=%d out=%q", code, out)
	}
	if !strings.Contains(out, "1 restored, 1 removed, 1 pen closed") {
		t.Fatalf("rollback summary: %q", out)
	}
	body, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(body) != "precious" {
		t.Fatalf("content = %q", body)
	}
	if _, err := os.Stat(filepath.Join(root, "junk.txt")); !os.IsNotExist(err) {
		t.Fatal("added file survived rollback")
	}
}

func TestRollbackToPrefixOfOuterPen(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "base state")
	_, out, _ := run(t, root, "new", "-m", "outer")
	id := regexp.MustCompile(`p-[a-z0-9]+-[0-9a-f]{4}`).FindString(out)
	if id == "" {
		t.Fatalf("no pen id in %q", out)
	}
	writeTree(t, root, "a.txt", "edit1 state")
	bump(t, root, "a.txt")
	_, out2, _ := run(t, root, "new", "-m", "inner")
	inner := regexp.MustCompile(`p-[a-z0-9]+-[0-9a-f]{4}`).FindString(out2)
	writeTree(t, root, "a.txt", "edit2 state")
	bump(t, root, "a.txt")
	// Two pens opened microseconds apart share a long timestamp prefix, so
	// pick the shortest strict prefix of the outer id that is unambiguous.
	prefix := id[:len(id)-1]
	for n := 10; n < len(id); n++ {
		if !strings.HasPrefix(inner, id[:n]) {
			prefix = id[:n]
			break
		}
	}
	code, _, errB := run(t, root, "rollback", "--to", prefix)
	if code != ExitOK {
		t.Fatalf("rollback --to exit=%d err=%q", code, errB)
	}
	body, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if string(body) != "base state" {
		t.Fatalf("content = %q", body)
	}
}

func TestStatusJSONIsParseableAndComplete(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "v1")
	run(t, root, "new")
	writeTree(t, root, "b.txt", "added")
	code, out, _ := run(t, root, "--format", "json", "status")
	if code != ExitChanges {
		t.Fatalf("json status exit=%d", code)
	}
	var v struct {
		Pen     string `json:"pen"`
		Changes []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"changes"`
		Summary struct{ Added int } `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if v.Pen == "" || len(v.Changes) != 1 || v.Changes[0].Kind != "added" || v.Summary.Added != 1 {
		t.Fatalf("json payload: %+v", v)
	}
}

func TestListShowsStackedPensBottomToTop(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "x")
	run(t, root, "new", "-m", "first checkpoint")
	run(t, root, "new", "-m", "second checkpoint")
	code, out, _ := run(t, root, "list")
	if code != ExitOK {
		t.Fatalf("list exit=%d", code)
	}
	iFirst := strings.Index(out, "first checkpoint")
	iSecond := strings.Index(out, "second checkpoint")
	if iFirst < 0 || iSecond < 0 || iFirst > iSecond {
		t.Fatalf("list order wrong:\n%s", out)
	}
	if !strings.Contains(out, "2 pens open") {
		t.Fatalf("count line missing:\n%s", out)
	}
	run(t, root, "commit")
	run(t, root, "commit")
	code, out, _ = run(t, root, "list")
	if code != ExitOK || !strings.Contains(out, "no open pens") {
		t.Fatalf("empty list: exit=%d out=%q", code, out)
	}
}

func TestShowReportsPenAndLiveChanges(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "x")
	_, out, _ := run(t, root, "new", "-m", "inspect me")
	id := regexp.MustCompile(`p-[a-z0-9]+-[0-9a-f]{4}`).FindString(out)
	writeTree(t, root, "extra.txt", "y")
	code, out, _ := run(t, root, "show", id)
	if code != ExitOK {
		t.Fatalf("show exit=%d", code)
	}
	for _, want := range []string{"pen " + id, "note:    inspect me", "changes: 1 since snapshot"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show missing %q:\n%s", want, out)
		}
	}
}

func TestLogRecordsTheLifecycle(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "x")
	run(t, root, "new", "-m", "session start")
	run(t, root, "commit", "-m", "accepted")
	code, out, _ := run(t, root, "log")
	if code != ExitOK {
		t.Fatalf("log exit=%d", code)
	}
	if !strings.Contains(out, "opened") || !strings.Contains(out, "committed") {
		t.Fatalf("log content:\n%s", out)
	}
	if !strings.Contains(out, `"accepted"`) {
		t.Fatalf("commit note missing from log:\n%s", out)
	}
}

func TestGCReportsReclaimedObjects(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "some file content here")
	run(t, root, "new")
	run(t, root, "commit") // pen closed → its blob is now unreferenced
	code, out, _ := run(t, root, "gc")
	if code != ExitOK {
		t.Fatalf("gc exit=%d", code)
	}
	if !strings.Contains(out, "removed 1 unreferenced object,") {
		t.Fatalf("gc output: %q", out)
	}
}

func TestMissingWorkspaceIsARuntimeError(t *testing.T) {
	root := t.TempDir() // no `new` has run, no .cowpen exists
	code, _, errB := run(t, root, "status")
	if code != ExitRuntime {
		t.Fatalf("exit=%d, want %d (stderr %q)", code, ExitRuntime, errB)
	}
	if !strings.Contains(errB, "no open pen") {
		t.Fatalf("stderr: %q", errB)
	}
}

func TestVerifyFlagCatchesQuietEdit(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, "a.txt", "AAAA")
	pin := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(filepath.Join(root, "a.txt"), pin, pin)
	run(t, root, "new")
	writeTree(t, root, "a.txt", "BBBB")
	os.Chtimes(filepath.Join(root, "a.txt"), pin, pin) // same size, same mtime
	code, _, _ := run(t, root, "status")
	if code != ExitOK {
		t.Fatalf("fast path should miss the quiet edit, exit=%d", code)
	}
	code, out, _ := run(t, root, "status", "--verify")
	if code != ExitChanges || !strings.Contains(out, "M a.txt") {
		t.Fatalf("--verify must catch it: exit=%d out=%q", code, out)
	}
}
