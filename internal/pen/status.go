// Change detection: compare the live tree against a pen's manifest.
// The fast path trusts size+mtime+mode (like git's index); content is
// hashed only when metadata disagrees, so `cowpen status` on a clean
// 10k-file tree reads zero file bodies.
package pen

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/JaydenCJ/cowpen/internal/scan"
)

// Change kinds, in the order they sort within a path tie (never happens —
// one change per path).
const (
	KindAdded    = "added"
	KindModified = "modified"
	KindDeleted  = "deleted"
	KindMode     = "mode" // permissions changed, content identical
	KindType     = "type" // e.g. file replaced by symlink or directory
)

// Change is one detected difference between the manifest and the tree.
type Change struct {
	Path string      `json:"path"`
	Kind string      `json:"kind"`
	Old  *scan.Entry `json:"old,omitempty"` // manifest side (nil for added)
	New  *scan.Entry `json:"new,omitempty"` // live side (nil for deleted)
}

// Summary counts changes by kind.
type Summary struct {
	Added    int `json:"added"`
	Modified int `json:"modified"`
	Deleted  int `json:"deleted"`
	Mode     int `json:"mode"`
	Type     int `json:"type"`
}

// Total is the number of changed paths.
func (s Summary) Total() int { return s.Added + s.Modified + s.Deleted + s.Mode + s.Type }

// Summarize tallies a change list.
func Summarize(changes []Change) Summary {
	var s Summary
	for _, c := range changes {
		switch c.Kind {
		case KindAdded:
			s.Added++
		case KindModified:
			s.Modified++
		case KindDeleted:
			s.Deleted++
		case KindMode:
			s.Mode++
		case KindType:
			s.Type++
		}
	}
	return s
}

// Status compares the live tree with p's manifest and returns the sorted
// change list. When verify is true every file is re-hashed regardless of
// metadata, catching editors that preserve size and mtime.
func (w *Workspace) Status(p *Pen, verify bool) ([]Change, error) {
	live, err := scan.Tree(w.Root, w.ig, false)
	if err != nil {
		return nil, err
	}
	manifest := scan.ByPath(p.Entries)
	var changes []Change

	for i := range live {
		l := live[i]
		m, ok := manifest[l.Path]
		if !ok {
			changes = append(changes, Change{Path: l.Path, Kind: KindAdded, New: &live[i]})
			continue
		}
		delete(manifest, l.Path)
		c, err := w.compare(m, l, verify)
		if err != nil {
			return nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}
	for path := range manifest {
		m := manifest[path]
		changes = append(changes, Change{Path: path, Kind: KindDeleted, Old: &m})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

// compare decides whether one manifest entry differs from its live
// counterpart. It may hash the live file; when it does, the computed hash
// is cached on the returned live entry so diff/rollback reuse it.
func (w *Workspace) compare(m, l scan.Entry, verify bool) (*Change, error) {
	if m.Type != l.Type {
		return &Change{Path: l.Path, Kind: KindType, Old: &m, New: &l}, nil
	}
	switch m.Type {
	case scan.TypeDir:
		if m.Mode != l.Mode {
			return &Change{Path: l.Path, Kind: KindMode, Old: &m, New: &l}, nil
		}
		return nil, nil
	case scan.TypeSymlink:
		if m.Target != l.Target {
			return &Change{Path: l.Path, Kind: KindModified, Old: &m, New: &l}, nil
		}
		return nil, nil
	}
	// Regular file. Fast path: identical size+mtime+mode means unchanged
	// unless the caller demanded verification.
	metaSame := m.Size == l.Size && m.MtimeNS == l.MtimeNS
	if metaSame && !verify {
		if m.Mode != l.Mode {
			return &Change{Path: l.Path, Kind: KindMode, Old: &m, New: &l}, nil
		}
		return nil, nil
	}
	if m.Size != l.Size {
		return &Change{Path: l.Path, Kind: KindModified, Old: &m, New: &l}, nil
	}
	// Same size, different mtime (or verify): hash to decide. A tool that
	// rewrites a file with identical bytes must not count as a change.
	h, err := scan.HashFile(filepath.Join(w.Root, filepath.FromSlash(l.Path)))
	if err != nil {
		if os.IsNotExist(err) {
			// Deleted between scan and hash: report deleted.
			return &Change{Path: l.Path, Kind: KindDeleted, Old: &m}, nil
		}
		return nil, err
	}
	l.Hash = h
	if h != m.Hash {
		return &Change{Path: l.Path, Kind: KindModified, Old: &m, New: &l}, nil
	}
	if m.Mode != l.Mode {
		return &Change{Path: l.Path, Kind: KindMode, Old: &m, New: &l}, nil
	}
	return nil, nil
}

// Commit accepts everything changed since the top pen: the pen is closed,
// the tree keeps its current contents, and the decision is logged. Lower
// pens in the stack stay open, so an outer rollback can still undo further.
func (w *Workspace) Commit(note string) (*Pen, Summary, error) {
	if err := w.guardMutation(); err != nil {
		return nil, Summary{}, err
	}
	top, err := w.Top()
	if err != nil {
		return nil, Summary{}, err
	}
	changes, err := w.Status(top, false)
	if err != nil {
		return nil, Summary{}, err
	}
	sum := Summarize(changes)
	ids, err := w.Stack()
	if err != nil {
		return nil, Summary{}, err
	}
	if err := w.writeStack(ids[:len(ids)-1]); err != nil {
		return nil, Summary{}, err
	}
	if err := w.removePenFile(top.ID); err != nil {
		return nil, Summary{}, err
	}
	w.appendHistory(historyEvent{
		Event: "committed", Pen: top.ID, Note: note,
		Added: sum.Added, Modified: sum.Modified, Deleted: sum.Deleted,
	})
	return top, sum, nil
}
