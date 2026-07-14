// Package ignore implements the .cowpenignore pattern language: a small,
// predictable subset of gitignore semantics. Patterns decide which paths a
// pen snapshot tracks; everything the matcher excludes is invisible to
// status, diff, commit, and rollback alike, so the rules here are
// deliberately simple enough to reason about in one read.
//
// Supported syntax:
//
//	# comment            — ignored
//	build/               — trailing slash: directories only
//	/notes.txt           — leading slash: anchored to the workspace root
//	*.log                — '*' matches within one path segment
//	vendor/**            — '**' matches across segments
//	cache-?              — '?' matches one non-separator character
//
// A pattern with no '/' matches the basename at any depth (like gitignore).
// Negation ('!') is not supported in 0.1.0; the parser rejects it loudly
// rather than silently mis-matching.
package ignore

import (
	"fmt"
	"strings"
)

// Builtin patterns are always active: the pen's own metadata and VCS
// internals must never be snapshotted or rolled back.
var Builtin = []string{".cowpen/", ".git/"}

type pattern struct {
	segs     []string // slash-split pattern segments; "**" is a wildcard segment
	anchored bool     // leading '/': match from the root only
	dirOnly  bool     // trailing '/': match directories only
	line     int      // 1-based source line, for error messages
}

// Matcher answers "should this relative path be ignored?".
type Matcher struct {
	patterns []pattern
}

// New builds a Matcher from raw .cowpenignore content plus the builtin
// patterns. It returns an error naming the offending line for syntax the
// 0.1.0 language does not support.
func New(content string) (*Matcher, error) {
	m := &Matcher{}
	for _, p := range Builtin {
		pat, _ := parseLine(p, 0)
		m.patterns = append(m.patterns, *pat)
	}
	for i, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pat, err := parseLine(line, i+1)
		if err != nil {
			return nil, err
		}
		m.patterns = append(m.patterns, *pat)
	}
	return m, nil
}

func parseLine(line string, n int) (*pattern, error) {
	if strings.HasPrefix(line, "!") {
		return nil, fmt.Errorf("line %d: negation (!) is not supported in 0.1.0", n)
	}
	p := pattern{line: n}
	if strings.HasSuffix(line, "/") {
		p.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	if strings.HasPrefix(line, "/") {
		p.anchored = true
		line = strings.TrimPrefix(line, "/")
	}
	if line == "" {
		return nil, fmt.Errorf("line %d: empty pattern", n)
	}
	// A pattern containing a slash is anchored, matching gitignore.
	if strings.Contains(line, "/") {
		p.anchored = true
	}
	p.segs = strings.Split(line, "/")
	return &p, nil
}

// Match reports whether the slash-separated relative path is ignored.
// isDir must be true when the path names a directory, so that dir-only
// patterns ("build/") skip plain files of the same name.
func (m *Matcher) Match(rel string, isDir bool) bool {
	if rel == "" || rel == "." {
		return false
	}
	segs := strings.Split(rel, "/")
	for _, p := range m.patterns {
		if p.dirOnly && !isDir {
			// A dir-only pattern still ignores everything *inside* a
			// matching directory: check every ancestor.
			if matchAncestor(p, segs) {
				return true
			}
			continue
		}
		if matchPattern(p, segs) || matchAncestor(p, segs) {
			return true
		}
	}
	return false
}

// matchAncestor reports whether any strict ancestor directory of the path
// matches the pattern — ignoring a directory ignores its whole subtree.
func matchAncestor(p pattern, segs []string) bool {
	for i := 1; i < len(segs); i++ {
		if matchPattern(p, segs[:i]) {
			return true
		}
	}
	return false
}

func matchPattern(p pattern, segs []string) bool {
	if p.anchored {
		return matchSegs(p.segs, segs)
	}
	// Unanchored: the pattern may start at any depth.
	for i := 0; i < len(segs); i++ {
		if matchSegs(p.segs, segs[i:]) {
			return true
		}
	}
	return false
}

// matchSegs matches pattern segments against path segments, where the
// pattern segment "**" may swallow zero or more path segments.
func matchSegs(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		if len(pat) == 1 {
			// Trailing '**' matches contents, not the directory itself
			// (gitignore semantics for "dir/**").
			return len(path) >= 1
		}
		for skip := 0; skip <= len(path); skip++ {
			if matchSegs(pat[1:], path[skip:]) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		return false
	}
	if !matchSeg(pat[0], path[0]) {
		return false
	}
	return matchSegs(pat[1:], path[1:])
}

// matchSeg matches a single pattern segment ('*' and '?' wildcards, no
// separator crossing) against one path segment.
func matchSeg(pat, s string) bool {
	// Iterative glob with single-star backtracking.
	var starPat, starS = -1, 0
	pi, si := 0, 0
	for si < len(s) {
		switch {
		case pi < len(pat) && (pat[pi] == '?' || pat[pi] == s[si]):
			pi++
			si++
		case pi < len(pat) && pat[pi] == '*':
			starPat, starS = pi, si
			pi++
		case starPat >= 0:
			starS++
			pi, si = starPat+1, starS
		default:
			return false
		}
	}
	for pi < len(pat) && pat[pi] == '*' {
		pi++
	}
	return pi == len(pat)
}
