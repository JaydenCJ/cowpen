// Package pen is cowpen's engine: it opens throwaway copy-on-write
// workspaces ("pens") over a directory tree, detects what an agent changed,
// renders diffs, and commits or rolls the tree back atomically. Everything
// here is plain userspace file I/O — no containers, no overlayfs, no
// syscall interception — so a pen works on any filesystem you can read.
package pen

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/JaydenCJ/cowpen/internal/ignore"
	"github.com/JaydenCJ/cowpen/internal/scan"
	"github.com/JaydenCJ/cowpen/internal/store"
)

// MetaDir is the workspace metadata directory, created at the root.
const MetaDir = ".cowpen"

// IgnoreFile is the optional per-workspace ignore file at the root.
const IgnoreFile = ".cowpenignore"

// ErrNoWorkspace is returned when no .cowpen directory exists here or in
// any parent directory.
var ErrNoWorkspace = errors.New("no cowpen workspace found (run `cowpen new` to open one)")

// ErrNoPen is returned by operations that need at least one open pen.
var ErrNoPen = errors.New("no open pen (run `cowpen new` first)")

// ErrJournal is returned when an interrupted rollback must be finished
// before anything else can mutate the tree.
var ErrJournal = errors.New("an interrupted rollback is pending; run `cowpen rollback --resume`")

// Workspace is an opened cowpen root: the tree under Root plus the
// metadata in Root/.cowpen.
type Workspace struct {
	Root  string
	store *store.Store
	ig    *ignore.Matcher
}

// Discover walks up from start looking for an existing workspace.
func Discover(start string) (*Workspace, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, err
	}
	for {
		if fi, err := os.Stat(filepath.Join(dir, MetaDir)); err == nil && fi.IsDir() {
			return Open(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNoWorkspace
		}
		dir = parent
	}
}

// Open opens (or initializes) the workspace rooted exactly at root.
func Open(root string) (*Workspace, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("workspace root %s is not a directory", root)
	}
	s, err := store.Open(filepath.Join(root, MetaDir, "objects"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, MetaDir, "pens"), 0o755); err != nil {
		return nil, err
	}
	ig, err := loadIgnore(root)
	if err != nil {
		return nil, err
	}
	return &Workspace{Root: root, store: s, ig: ig}, nil
}

func loadIgnore(root string) (*ignore.Matcher, error) {
	content := ""
	if b, err := os.ReadFile(filepath.Join(root, IgnoreFile)); err == nil {
		content = string(b)
	}
	m, err := ignore.New(content)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", IgnoreFile, err)
	}
	return m, nil
}

// ---- pen manifests ----

// Pen is one snapshot checkpoint: the full manifest of the tree at the
// moment it was opened. Pens stack; rollback restores a pen's manifest.
type Pen struct {
	ID      string       `json:"id"`
	Note    string       `json:"note,omitempty"`
	Created time.Time    `json:"created"`
	Files   int          `json:"files"`
	Bytes   int64        `json:"bytes"`
	Entries []scan.Entry `json:"entries"`
}

func (w *Workspace) penPath(id string) string {
	return filepath.Join(w.Root, MetaDir, "pens", id+".json")
}

func (w *Workspace) stackPath() string   { return filepath.Join(w.Root, MetaDir, "stack.json") }
func (w *Workspace) historyPath() string { return filepath.Join(w.Root, MetaDir, "history.jsonl") }
func (w *Workspace) journalPath() string { return filepath.Join(w.Root, MetaDir, "journal.json") }

// Stack returns the open pen IDs, bottom (oldest) to top (newest).
func (w *Workspace) Stack() ([]string, error) {
	b, err := os.ReadFile(w.stackPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		return nil, fmt.Errorf("corrupt stack.json: %w", err)
	}
	return ids, nil
}

func (w *Workspace) writeStack(ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	return atomicWrite(w.stackPath(), b, 0o644)
}

// LoadPen reads one pen manifest by ID. A unique ID prefix is accepted,
// so humans can type the short form printed by `cowpen list`.
func (w *Workspace) LoadPen(id string) (*Pen, error) {
	full, err := w.resolveID(id)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(w.penPath(full))
	if err != nil {
		return nil, fmt.Errorf("pen %s: %w", id, err)
	}
	var p Pen
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("pen %s: corrupt manifest: %w", id, err)
	}
	return &p, nil
}

func (w *Workspace) resolveID(id string) (string, error) {
	ids, err := w.Stack()
	if err != nil {
		return "", err
	}
	var matches []string
	for _, candidate := range ids {
		if candidate == id {
			return id, nil
		}
		if strings.HasPrefix(candidate, id) {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("no open pen matches %q", id)
	default:
		return "", fmt.Errorf("pen id %q is ambiguous (%s)", id, strings.Join(matches, ", "))
	}
}

// Top returns the most recent open pen.
func (w *Workspace) Top() (*Pen, error) {
	ids, err := w.Stack()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, ErrNoPen
	}
	return w.LoadPen(ids[len(ids)-1])
}

// HasJournal reports whether an interrupted rollback is pending.
func (w *Workspace) HasJournal() bool {
	_, err := os.Stat(w.journalPath())
	return err == nil
}

// guardMutation refuses to proceed while a rollback journal is pending.
func (w *Workspace) guardMutation() error {
	if w.HasJournal() {
		return ErrJournal
	}
	return nil
}

// ---- pen creation ----

// NewResult summarizes what NewPen snapshotted.
type NewResult struct {
	Pen         *Pen
	StoredBytes int64 // bytes newly written to the object store
	Deduped     int   // files whose content was already stored
}

// NewPen scans the tree, stores every file body in the content-addressed
// store (deduplicated), writes the manifest, and pushes the pen onto the
// stack. The tree itself is untouched: the agent keeps editing in place.
func (w *Workspace) NewPen(note string) (*NewResult, error) {
	if err := w.guardMutation(); err != nil {
		return nil, err
	}
	entries, err := scan.Tree(w.Root, w.ig, true)
	if err != nil {
		return nil, err
	}
	res := &NewResult{}
	p := &Pen{ID: newID(), Note: note, Created: time.Now().UTC(), Entries: entries}
	for _, e := range entries {
		if e.Type != scan.TypeFile {
			continue
		}
		p.Files++
		p.Bytes += e.Size
		if w.store.Has(e.Hash) {
			res.Deduped++
			continue
		}
		// The file may change between hashing and storing; PutFile
		// re-hashes the stream, so trust its result over the scan's.
		h, err := w.store.PutFile(filepath.Join(w.Root, filepath.FromSlash(e.Path)))
		if err != nil {
			return nil, err
		}
		if h != e.Hash {
			return nil, fmt.Errorf("%s changed while snapshotting; retry when the tree is quiet", e.Path)
		}
		res.StoredBytes += e.Size
	}
	if err := w.savePen(p); err != nil {
		return nil, err
	}
	ids, err := w.Stack()
	if err != nil {
		return nil, err
	}
	if err := w.writeStack(append(ids, p.ID)); err != nil {
		return nil, err
	}
	res.Pen = p
	w.appendHistory(historyEvent{Event: "opened", Pen: p.ID, Note: note, Files: p.Files})
	return res, nil
}

func (w *Workspace) savePen(p *Pen) error {
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(w.penPath(p.ID), b, 0o644)
}

func (w *Workspace) removePenFile(id string) error {
	return os.Remove(w.penPath(id))
}

// newID builds a sortable, collision-resistant pen ID:
// p-<unixnano base36>-<4 random hex chars>.
func newID() string {
	var r [2]byte
	_, _ = rand.Read(r[:])
	return "p-" + strconv.FormatInt(time.Now().UnixNano(), 36) + "-" + hex.EncodeToString(r[:])
}

// ---- history ----

type historyEvent struct {
	Time     time.Time `json:"time"`
	Event    string    `json:"event"` // opened | committed | rolled_back
	Pen      string    `json:"pen"`
	Note     string    `json:"note,omitempty"`
	Files    int       `json:"files,omitempty"`
	Added    int       `json:"added,omitempty"`
	Modified int       `json:"modified,omitempty"`
	Deleted  int       `json:"deleted,omitempty"`
}

func (w *Workspace) appendHistory(ev historyEvent) {
	ev.Time = time.Now().UTC()
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(w.historyPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// History returns every recorded event, oldest first.
func (w *Workspace) History() ([]historyEvent, error) {
	b, err := os.ReadFile(w.historyPath())
	if errors.Is(err, os.ErrNotExist) {
		return []historyEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	evs := []historyEvent{} // non-nil so `log --format json` emits [], not null
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var ev historyEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // a torn tail line must not brick `cowpen log`
		}
		evs = append(evs, ev)
	}
	return evs, nil
}

// atomicWrite writes b to path via a temp file + rename, so readers never
// observe a half-written metadata file.
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cowpen-w-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
