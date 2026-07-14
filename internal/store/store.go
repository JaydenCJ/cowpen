// Package store is cowpen's content-addressed blob store: every snapshotted
// file body lives exactly once under .cowpen/objects/<aa>/<rest-of-sha256>.
// Identical content across pens, paths, and time is stored once — that
// deduplication is what makes stacked checkpoints cheap. Writes go through
// a temp file and an atomic rename, so a crash can leave stray temp files
// but never a truncated object.
package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store is a directory of immutable, hash-named blobs.
type Store struct {
	dir string
}

// Open creates the store directory if needed and returns a handle.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Path returns where the blob for hash lives (whether or not it exists).
func (s *Store) Path(hash string) string {
	return filepath.Join(s.dir, hash[:2], hash[2:])
}

// Has reports whether a blob is present.
func (s *Store) Has(hash string) bool {
	_, err := os.Stat(s.Path(hash))
	return err == nil
}

// PutFile stores the content of the file at src and returns its SHA-256.
// Content is hashed while it streams to a temp file; if the object already
// exists the temp file is discarded, so repeated snapshots of unchanged
// trees cost one read and zero new bytes.
func (s *Store) PutFile(src string) (string, error) {
	f, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return s.put(f)
}

// PutBytes stores an in-memory blob (used by tests and small manifests).
func (s *Store) PutBytes(b []byte) (string, error) {
	return s.put(bytes.NewReader(b))
}

func (s *Store) put(r io.Reader) (string, error) {
	tmp, err := os.CreateTemp(s.dir, "tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	dst := s.Path(hash)
	if s.Has(hash) {
		return hash, nil // dedup: identical content already stored
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	// Objects are immutable: drop write bits before publishing.
	if err := os.Chmod(tmp.Name(), 0o444); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return hash, nil
}

// Open returns a reader over the blob for hash.
func (s *Store) Open(hash string) (io.ReadCloser, error) {
	f, err := os.Open(s.Path(hash))
	if err != nil {
		return nil, fmt.Errorf("object %s: %w", short(hash), err)
	}
	return f, nil
}

// ReadAll loads a whole blob into memory (diffs need both sides in RAM).
func (s *Store) ReadAll(hash string) ([]byte, error) {
	r, err := s.Open(hash)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// List returns every stored hash, sorted. Used by gc.
func (s *Store) List() ([]string, error) {
	var hashes []string
	err := filepath.WalkDir(s.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, "tmp-") {
			return nil // stray temp from an interrupted write; gc removes it
		}
		prefix := filepath.Base(filepath.Dir(p))
		if len(prefix) == 2 && len(name) == 62 {
			hashes = append(hashes, prefix+name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(hashes)
	return hashes, nil
}

// Remove deletes one blob (gc only). Returns the bytes freed.
func (s *Store) Remove(hash string) (int64, error) {
	p := s.Path(hash)
	info, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	if err := os.Remove(p); err != nil {
		return 0, err
	}
	// Best-effort: drop the two-char fan-out dir if now empty.
	os.Remove(filepath.Dir(p))
	return info.Size(), nil
}

// Sweep removes stray temp files left by interrupted writes and returns
// how many were cleaned up.
func (s *Store) Sweep() (int, error) {
	matches, err := filepath.Glob(filepath.Join(s.dir, "tmp-*"))
	if err != nil {
		return 0, err
	}
	for _, m := range matches {
		if err := os.Remove(m); err != nil {
			return 0, err
		}
	}
	return len(matches), nil
}

func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
