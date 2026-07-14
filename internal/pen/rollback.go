// Atomic rollback. Restoring a pen runs in two phases:
//
//  1. Prepare — every file body to restore is copied from the object store
//     into a temp file *next to its destination* (same directory, so the
//     final rename cannot cross filesystems). Any failure here aborts with
//     the tree completely untouched.
//  2. Apply — a journal listing every step is written first, then the
//     steps run: mkdirs, atomic renames, symlink swaps, removals of added
//     files, chmods, and empty-dir cleanup. Each step is idempotent, so if
//     the process dies mid-apply, `cowpen rollback --resume` replays the
//     journal to completion. The tree is never left in a state the journal
//     cannot finish from.
package pen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/JaydenCJ/cowpen/internal/scan"
)

// Step ops, in apply order within the plan.
const (
	opMkdir      = "mkdir"       // ensure a directory exists with mode
	opRename     = "rename"      // move a prepared temp into place
	opSymlink    = "symlink"     // (re)create a symlink to Target
	opRemoveFile = "remove_file" // delete an added file or symlink
	opChmod      = "chmod"       // restore permissions
	opRmdir      = "rmdir"       // remove an added directory if empty
)

// Step is one journaled rollback action. Paths are relative to the root.
type Step struct {
	Op     string `json:"op"`
	Path   string `json:"path"`
	Tmp    string `json:"tmp,omitempty"`    // rename: prepared temp file
	Hash   string `json:"hash,omitempty"`   // rename: expected content
	Target string `json:"target,omitempty"` // symlink target
	Mode   uint32 `json:"mode,omitempty"`
}

type journal struct {
	Pen       string   `json:"pen"`        // pen being restored
	ClosePens []string `json:"close_pens"` // stack entries to pop when done
	Steps     []Step   `json:"steps"`
}

// RollbackResult reports what a completed rollback did.
type RollbackResult struct {
	Pen      *Pen
	Restored int      // file bodies and symlinks put back
	Removed  int      // added files/dirs deleted
	Closed   int      // pens popped from the stack
	Skipped  []string // added dirs left in place because they weren't empty
}

// Rollback restores the tree to the state of the pen with the given ID
// (or the top pen when id is ""). The named pen and every pen above it
// are closed. The restore is journaled and idempotent.
func (w *Workspace) Rollback(id string) (*RollbackResult, error) {
	if w.HasJournal() {
		return nil, ErrJournal
	}
	ids, err := w.Stack()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNoPen
	}
	target := ids[len(ids)-1]
	if id != "" {
		target, err = w.resolveID(id)
		if err != nil {
			return nil, err
		}
	}
	idx := -1
	for i, s := range ids {
		if s == target {
			idx = i
			break
		}
	}
	p, err := w.LoadPen(target)
	if err != nil {
		return nil, err
	}
	changes, err := w.Status(p, false)
	if err != nil {
		return nil, err
	}
	j := &journal{Pen: p.ID, ClosePens: ids[idx:]}
	if err := w.plan(j, p, changes); err != nil {
		w.discardTemps(j)
		return nil, err
	}
	if len(j.Steps) > 0 {
		b, err := json.MarshalIndent(j, "", "  ")
		if err != nil {
			w.discardTemps(j)
			return nil, err
		}
		if err := atomicWrite(w.journalPath(), b, 0o644); err != nil {
			w.discardTemps(j)
			return nil, err
		}
	}
	return w.applyJournal(j, p)
}

// Resume finishes a rollback whose apply phase was interrupted.
func (w *Workspace) Resume() (*RollbackResult, error) {
	b, err := os.ReadFile(w.journalPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("nothing to resume: no rollback journal found")
	}
	if err != nil {
		return nil, err
	}
	var j journal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("corrupt journal: %w", err)
	}
	p, err := w.LoadPen(j.Pen)
	if err != nil {
		return nil, err
	}
	return w.applyJournal(&j, p)
}

// plan turns the change list into journal steps and prepares temp files.
// Nothing outside .cowpen and the temp files is touched.
func (w *Workspace) plan(j *journal, p *Pen, changes []Change) error {
	manifest := scan.ByPath(p.Entries)
	var mkdirs, renames, symlinks, removes, chmods, rmdirs []Step

	for _, c := range changes {
		switch {
		case c.Kind == KindAdded:
			if c.New.Type == scan.TypeDir {
				rmdirs = append(rmdirs, Step{Op: opRmdir, Path: c.Path})
			} else {
				removes = append(removes, Step{Op: opRemoveFile, Path: c.Path})
			}
		case c.Kind == KindMode:
			chmods = append(chmods, Step{Op: opChmod, Path: c.Path, Mode: c.Old.Mode})
		default: // modified, deleted, type — restore the manifest entry
			old := c.Old
			if c.New != nil && c.Kind != KindDeleted && c.Old.Type != c.New.Type {
				// Type change: the live object must go before the restore.
				if c.New.Type == scan.TypeDir {
					rmdirs = append(rmdirs, Step{Op: opRmdir, Path: c.Path})
				} else {
					removes = append(removes, Step{Op: opRemoveFile, Path: c.Path})
				}
			}
			switch old.Type {
			case scan.TypeFile:
				tmp, err := w.prepareTemp(old)
				if err != nil {
					return err
				}
				renames = append(renames, Step{Op: opRename, Path: old.Path, Tmp: tmp, Hash: old.Hash})
			case scan.TypeSymlink:
				symlinks = append(symlinks, Step{Op: opSymlink, Path: old.Path, Target: old.Target})
			case scan.TypeDir:
				mkdirs = append(mkdirs, Step{Op: opMkdir, Path: old.Path, Mode: old.Mode})
			}
		}
	}

	// Restored files need their parent directories back (a deleted dir may
	// contain deleted files). Collect every missing ancestor tracked by
	// the manifest, plus untracked ancestors with a default mode.
	need := map[string]uint32{}
	for _, s := range append(append([]Step{}, renames...), symlinks...) {
		for dir := parent(s.Path); dir != ""; dir = parent(dir) {
			if _, ok := need[dir]; ok {
				continue
			}
			mode := uint32(0o755)
			if m, ok := manifest[dir]; ok && m.Type == scan.TypeDir {
				mode = m.Mode
			}
			need[dir] = mode
		}
	}
	for dir, mode := range need {
		mkdirs = append(mkdirs, Step{Op: opMkdir, Path: dir, Mode: mode})
	}
	// Shallow dirs first so parents exist before children.
	sort.Slice(mkdirs, func(a, b int) bool { return depth(mkdirs[a].Path) < depth(mkdirs[b].Path) })
	// Deep dirs first so nested added dirs empty out bottom-up.
	sort.Slice(rmdirs, func(a, b int) bool { return depth(rmdirs[a].Path) > depth(rmdirs[b].Path) })

	// Apply order: create dirs, remove added files (frees rmdir targets),
	// restore bodies, restore symlinks, fix modes, then prune added dirs.
	j.Steps = append(j.Steps, mkdirs...)
	j.Steps = append(j.Steps, removes...)
	j.Steps = append(j.Steps, renames...)
	j.Steps = append(j.Steps, symlinks...)
	j.Steps = append(j.Steps, chmods...)
	j.Steps = append(j.Steps, rmdirs...)
	return nil
}

// prepareTemp copies the blob for e into a temp file in the destination
// directory, with the manifest's mode already set, and returns the temp's
// root-relative path.
func (w *Workspace) prepareTemp(e *scan.Entry) (string, error) {
	dstDir := filepath.Join(w.Root, filepath.FromSlash(parent(e.Path)))
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dstDir, ".cowpen-restore-*")
	if err != nil {
		return "", err
	}
	src, err := w.store.Open(e.Hash)
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	_, cpErr := io.Copy(tmp, src)
	src.Close()
	if cpErr == nil {
		cpErr = tmp.Close()
	} else {
		tmp.Close()
	}
	if cpErr == nil {
		cpErr = os.Chmod(tmp.Name(), os.FileMode(e.Mode))
	}
	if cpErr == nil {
		// Restore the snapshot's mtime too, so a still-open outer pen's
		// fast path sees the file exactly as it recorded it.
		mt := timeFromNS(e.MtimeNS)
		cpErr = os.Chtimes(tmp.Name(), mt, mt)
	}
	if cpErr != nil {
		os.Remove(tmp.Name())
		return "", cpErr
	}
	rel, err := filepath.Rel(w.Root, tmp.Name())
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// discardTemps removes prepared temp files after a failed plan.
func (w *Workspace) discardTemps(j *journal) {
	for _, s := range j.Steps {
		if s.Op == opRename && s.Tmp != "" {
			os.Remove(filepath.Join(w.Root, filepath.FromSlash(s.Tmp)))
		}
	}
}

// applyJournal executes every step, closes the pens, and clears the
// journal. Every step tolerates having already run, which is what makes
// resume safe.
func (w *Workspace) applyJournal(j *journal, p *Pen) (*RollbackResult, error) {
	res := &RollbackResult{Pen: p}
	abs := func(rel string) string { return filepath.Join(w.Root, filepath.FromSlash(rel)) }

	for _, s := range j.Steps {
		switch s.Op {
		case opMkdir:
			if err := os.MkdirAll(abs(s.Path), os.FileMode(s.Mode)); err != nil {
				return nil, err
			}
			if err := os.Chmod(abs(s.Path), os.FileMode(s.Mode)); err != nil {
				return nil, err
			}
		case opRemoveFile:
			if err := os.Remove(abs(s.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			res.Removed++
		case opRename:
			if _, err := os.Lstat(abs(s.Tmp)); errors.Is(err, os.ErrNotExist) {
				// Resume path: the rename already happened iff the
				// destination carries the expected content.
				h, err := scan.HashFile(abs(s.Path))
				if err != nil || h != s.Hash {
					return nil, fmt.Errorf("resume: %s: temp gone and destination does not match snapshot", s.Path)
				}
				res.Restored++
				continue
			}
			if err := os.Rename(abs(s.Tmp), abs(s.Path)); err != nil {
				return nil, err
			}
			res.Restored++
		case opSymlink:
			if cur, err := os.Readlink(abs(s.Path)); err == nil && cur == s.Target {
				res.Restored++
				continue
			}
			if err := os.Remove(abs(s.Path)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			if err := os.Symlink(s.Target, abs(s.Path)); err != nil {
				return nil, err
			}
			res.Restored++
		case opChmod:
			if err := os.Chmod(abs(s.Path), os.FileMode(s.Mode)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		case opRmdir:
			err := os.Remove(abs(s.Path))
			switch {
			case err == nil:
				res.Removed++
			case errors.Is(err, os.ErrNotExist):
				// already gone (resume)
			case isNotEmpty(err):
				// The added dir holds ignored/untracked files; leaving it
				// is safer than deleting content the pen never saw.
				res.Skipped = append(res.Skipped, s.Path)
			default:
				return nil, err
			}
		default:
			return nil, fmt.Errorf("journal: unknown op %q", s.Op)
		}
	}

	// Finish: pop the closed pens, then clear the journal. If we crash
	// between these, resume re-runs the (idempotent) steps and re-pops.
	ids, err := w.Stack()
	if err != nil {
		return nil, err
	}
	var remaining []string
	closed := map[string]bool{}
	for _, c := range j.ClosePens {
		closed[c] = true
	}
	for _, id := range ids {
		if !closed[id] {
			remaining = append(remaining, id)
		} else {
			res.Closed++
		}
	}
	if err := w.writeStack(remaining); err != nil {
		return nil, err
	}
	for _, id := range j.ClosePens {
		if err := w.removePenFile(id); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if err := os.Remove(w.journalPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	w.appendHistory(historyEvent{Event: "rolled_back", Pen: p.ID, Files: res.Restored})
	return res, nil
}

func timeFromNS(ns int64) time.Time { return time.Unix(0, ns) }

func isNotEmpty(err error) bool {
	return strings.Contains(err.Error(), "not empty")
}

func parent(rel string) string {
	i := strings.LastIndex(rel, "/")
	if i < 0 {
		return ""
	}
	return rel[:i]
}

func depth(rel string) int { return strings.Count(rel, "/") }
