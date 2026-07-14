// Tests for the .cowpenignore pattern language. Every case documents a
// semantic promise the README makes; a change that breaks one of these
// silently changes which files a pen protects.
package ignore

import "testing"

func mustNew(t *testing.T, content string) *Matcher {
	t.Helper()
	m, err := New(content)
	if err != nil {
		t.Fatalf("New(%q): %v", content, err)
	}
	return m
}

func TestBuiltinMetadataDirsAlwaysIgnored(t *testing.T) {
	m := mustNew(t, "")
	if !m.Match(".cowpen", true) {
		t.Fatal(".cowpen dir must be ignored with no user patterns")
	}
	if !m.Match(".cowpen/objects/ab/cdef", false) {
		t.Fatal("files under .cowpen must be ignored")
	}
	if !m.Match(".git/HEAD", false) {
		t.Fatal("VCS internals must never be snapshotted")
	}
}

func TestBasenamePatternMatchesAtAnyDepth(t *testing.T) {
	m := mustNew(t, "*.log")
	for _, p := range []string{"a.log", "deep/nested/b.log"} {
		if !m.Match(p, false) {
			t.Errorf("%q should match *.log", p)
		}
	}
	if m.Match("a.log.txt", false) {
		t.Error("suffix must anchor: a.log.txt is not *.log")
	}
}

func TestStarDoesNotCrossSeparator(t *testing.T) {
	m := mustNew(t, "/build*")
	if !m.Match("build-cache", true) {
		t.Error("build-cache should match /build*")
	}
	if m.Match("src/build-cache", true) {
		t.Error("anchored pattern must not match at depth")
	}
}

func TestDoubleStarCrossesSeparators(t *testing.T) {
	m := mustNew(t, "vendor/**")
	if !m.Match("vendor/a/b/c.go", false) {
		t.Error("vendor/** should match deep descendants")
	}
	if m.Match("vendor", true) {
		t.Error("vendor/** matches contents, not the dir itself")
	}
}

func TestDirOnlyPatternSkipsFilesButIgnoresSubtrees(t *testing.T) {
	m := mustNew(t, "build/")
	if m.Match("build", false) {
		t.Error("a plain file named build must not match build/")
	}
	if !m.Match("build", true) {
		t.Error("the directory build must match build/")
	}
	if !m.Match("build/out.o", false) {
		t.Error("files inside an ignored dir are ignored too")
	}
	m = mustNew(t, "node_modules/")
	if !m.Match("a/b/node_modules/pkg/index.js", false) {
		t.Error("nested ignored dirs must ignore their whole subtree")
	}
}

func TestAnchoredPatternsOnlyMatchFromRoot(t *testing.T) {
	m := mustNew(t, "/notes.txt")
	if !m.Match("notes.txt", false) {
		t.Error("/notes.txt should match at the root")
	}
	if m.Match("sub/notes.txt", false) {
		t.Error("/notes.txt must not match in subdirectories")
	}
	// gitignore rule: any slash in the pattern makes it root-relative.
	m = mustNew(t, "docs/tmp")
	if !m.Match("docs/tmp", true) {
		t.Error("docs/tmp should match from the root")
	}
	if m.Match("x/docs/tmp", true) {
		t.Error("a slashed pattern must not float to other depths")
	}
}

func TestQuestionMarkMatchesOneCharacter(t *testing.T) {
	m := mustNew(t, "cache-?")
	if !m.Match("cache-1", false) || !m.Match("sub/cache-x", false) {
		t.Error("? should match exactly one character")
	}
	if m.Match("cache-10", false) || m.Match("cache-", false) {
		t.Error("? must match exactly one character, not zero or two")
	}
}

func TestCommentsAndBlankLinesAreSkipped(t *testing.T) {
	m := mustNew(t, "# a comment\n\n*.tmp\n   \n# another\n")
	if !m.Match("x.tmp", false) {
		t.Error("patterns around comments should still work")
	}
	if m.Match("# a comment", false) {
		t.Error("comment text is not a pattern")
	}
}

func TestNegationIsRejectedLoudly(t *testing.T) {
	if _, err := New("!keep.log"); err == nil {
		t.Fatal("negation must be rejected in 0.1.0, not silently ignored")
	}
}

func TestUnrelatedPathsAreNotIgnored(t *testing.T) {
	m := mustNew(t, "*.log\nbuild/\n/secret")
	for _, p := range []string{"main.go", "src/log.go", "builder/x", "sub/secret"} {
		if m.Match(p, false) {
			t.Errorf("%q must not be ignored", p)
		}
	}
}
