// Package difftext produces unified diffs between two byte slices using
// Myers' O(ND) shortest-edit-script algorithm. It is what `cowpen diff`
// prints: git-compatible hunk headers, three lines of context, correct
// "\ No newline at end of file" markers, and a conservative binary
// detector so object files never explode into line noise.
package difftext

import (
	"bytes"
	"fmt"
	"strings"
)

// DefaultContext is the number of unchanged lines shown around each hunk.
const DefaultContext = 3

// IsBinary reports whether content should be treated as binary: a NUL
// byte anywhere in the first 8000 bytes (the same heuristic git uses).
func IsBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

// Unified renders a unified diff of a→b with the given context width.
// aName/bName become the ---/+++ header labels. It returns "" when the
// inputs are byte-identical.
func Unified(aName, bName string, a, b []byte, context int) string {
	if bytes.Equal(a, b) {
		return ""
	}
	al := splitLines(a)
	bl := splitLines(b)
	ops := diffLines(al, bl)
	hunks := groupHunks(ops, context)
	if len(hunks) == 0 {
		return ""
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
	for _, h := range hunks {
		fmt.Fprintf(&sb, "@@ -%s +%s @@\n", spanLabel(h.aStart, h.aLen), spanLabel(h.bStart, h.bLen))
		for _, op := range h.ops {
			switch op.kind {
			case opEqual:
				writeLine(&sb, ' ', al[op.aIdx])
			case opDelete:
				writeLine(&sb, '-', al[op.aIdx])
			case opInsert:
				writeLine(&sb, '+', bl[op.bIdx])
			}
		}
	}
	return sb.String()
}

// splitLines splits content into lines that KEEP their terminators, so a
// final line with and without a trailing newline compare as different —
// exactly the distinction the "\ No newline" marker exists for.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(b), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func writeLine(sb *strings.Builder, prefix byte, line string) {
	noNL := !strings.HasSuffix(line, "\n")
	sb.WriteByte(prefix)
	sb.WriteString(strings.TrimSuffix(line, "\n"))
	sb.WriteByte('\n')
	if noNL {
		sb.WriteString("\\ No newline at end of file\n")
	}
}

func spanLabel(start, length int) string {
	// Unified format: a zero-length span is reported at the line *before*
	// the change; a one-length span omits the count.
	if length == 0 {
		return fmt.Sprintf("%d,0", start)
	}
	if length == 1 {
		return fmt.Sprintf("%d", start+1)
	}
	return fmt.Sprintf("%d,%d", start+1, length)
}

const (
	opEqual = iota
	opDelete
	opInsert
)

type op struct {
	kind int
	aIdx int // index into a's lines (equal/delete)
	bIdx int // index into b's lines (equal/insert)
}

// diffLines computes the edit script with Myers' greedy algorithm, after
// trimming the common prefix and suffix so typical "one function changed"
// diffs never touch the O(ND) core with the whole file.
func diffLines(a, b []string) []op {
	// Common prefix.
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	// Common suffix (not overlapping the prefix).
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	mid := myers(a[pre:len(a)-suf], b[pre:len(b)-suf])

	ops := make([]op, 0, pre+len(mid)+suf)
	for i := 0; i < pre; i++ {
		ops = append(ops, op{opEqual, i, i})
	}
	for _, o := range mid {
		o.aIdx += pre
		o.bIdx += pre
		ops = append(ops, o)
	}
	for i := 0; i < suf; i++ {
		ops = append(ops, op{opEqual, len(a) - suf + i, len(b) - suf + i})
	}
	return ops
}

// myers is the textbook O(ND) forward algorithm with a traceback.
func myers(a, b []string) []op {
	n, m := len(a), len(b)
	if n == 0 && m == 0 {
		return nil
	}
	max := n + m
	// v[k+max] = furthest x on diagonal k; trace snapshots v per d step.
	v := make([]int, 2*max+2)
	var trace [][]int
	var dFound = -1
search:
	for d := 0; d <= max; d++ {
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+max] < v[k+1+max]) {
				x = v[k+1+max] // move down (insert from b)
			} else {
				x = v[k-1+max] + 1 // move right (delete from a)
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+max] = x
			if x >= n && y >= m {
				dFound = d
				break search
			}
		}
	}

	// Traceback from (n, m) to (0, 0).
	var rev []op
	x, y := n, m
	for d := dFound; d > 0; d-- {
		vPrev := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && vPrev[k-1+max] < vPrev[k+1+max]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := vPrev[prevK+max]
		prevY := prevX - prevK
		for x > prevX && y > prevY { // snake: equal lines
			rev = append(rev, op{opEqual, x - 1, y - 1})
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, op{opInsert, -1, y - 1})
			y--
		} else {
			rev = append(rev, op{opDelete, x - 1, -1})
			x--
		}
	}
	for x > 0 && y > 0 { // leading snake at d=0
		rev = append(rev, op{opEqual, x - 1, y - 1})
		x--
		y--
	}
	// Reverse in place.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

type hunk struct {
	aStart, aLen int
	bStart, bLen int
	ops          []op
}

// groupHunks trims runs of equal lines down to the context width and
// merges changes whose context would overlap into a single hunk.
func groupHunks(ops []op, context int) []hunk {
	// Indices of non-equal ops.
	var changed []int
	for i, o := range ops {
		if o.kind != opEqual {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	var hunks []hunk
	i := 0
	for i < len(changed) {
		start := changed[i] - context
		if start < 0 {
			start = 0
		}
		end := changed[i] + context // inclusive bound grows as we merge
		j := i
		for j+1 < len(changed) && changed[j+1]-context <= end+1 {
			j++
			end = changed[j] + context
		}
		if end > len(ops)-1 {
			end = len(ops) - 1
		}
		h := hunk{ops: ops[start : end+1]}
		h.aStart, h.bStart = lineStarts(ops, start)
		for _, o := range h.ops {
			if o.kind != opInsert {
				h.aLen++
			}
			if o.kind != opDelete {
				h.bLen++
			}
		}
		hunks = append(hunks, h)
		i = j + 1
	}
	return hunks
}

// lineStarts returns how many a-lines and b-lines precede ops[idx].
func lineStarts(ops []op, idx int) (aStart, bStart int) {
	for _, o := range ops[:idx] {
		if o.kind != opInsert {
			aStart++
		}
		if o.kind != opDelete {
			bStart++
		}
	}
	return aStart, bStart
}
