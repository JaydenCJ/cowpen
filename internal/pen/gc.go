// Garbage collection: blobs referenced by no open pen are unreachable —
// nothing can restore from them — so gc deletes them and reports the
// space reclaimed. Stray temp files from interrupted store writes are
// swept in the same pass.
package pen

import (
	"github.com/JaydenCJ/cowpen/internal/scan"
)

// GCResult reports what a gc pass reclaimed.
type GCResult struct {
	Removed    int   `json:"removed"`
	FreedBytes int64 `json:"freed_bytes"`
	TempsSwept int   `json:"temps_swept"`
}

// GC removes every stored blob not referenced by an open pen. It refuses
// to run while a rollback journal is pending, since that rollback still
// needs its blobs.
func (w *Workspace) GC() (*GCResult, error) {
	if err := w.guardMutation(); err != nil {
		return nil, err
	}
	referenced := map[string]bool{}
	ids, err := w.Stack()
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		p, err := w.LoadPen(id)
		if err != nil {
			return nil, err
		}
		for _, e := range p.Entries {
			if e.Type == scan.TypeFile && e.Hash != "" {
				referenced[e.Hash] = true
			}
		}
	}
	all, err := w.store.List()
	if err != nil {
		return nil, err
	}
	res := &GCResult{}
	for _, h := range all {
		if referenced[h] {
			continue
		}
		n, err := w.store.Remove(h)
		if err != nil {
			return nil, err
		}
		res.Removed++
		res.FreedBytes += n
	}
	res.TempsSwept, err = w.store.Sweep()
	if err != nil {
		return nil, err
	}
	return res, nil
}
