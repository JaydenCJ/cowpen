// Package scan walks a workspace tree and produces the flat, sorted entry
// list that manifests, change detection, and rollback plans are all built
// from. It records exactly what a userspace snapshot needs — path, type,
// mode, size, mtime, content hash, symlink target — and nothing more.
package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/JaydenCJ/cowpen/internal/ignore"
)

// Entry types. Directories are tracked so empty dirs and dir modes survive
// a rollback; symlinks are recorded by target, never followed.
const (
	TypeFile    = "file"
	TypeDir     = "dir"
	TypeSymlink = "symlink"
)

// Entry is one tracked filesystem object, keyed by its slash-separated
// path relative to the workspace root.
type Entry struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Mode    uint32 `json:"mode"`           // permission bits only
	Size    int64  `json:"size,omitempty"` // files only
	MtimeNS int64  `json:"mtime_ns,omitempty"`
	Hash    string `json:"hash,omitempty"`   // sha256 hex, files only, when hashed
	Target  string `json:"target,omitempty"` // symlinks only
}

// Tree walks root and returns entries sorted by path. When hash is true,
// every regular file's SHA-256 is computed; when false the caller relies
// on size+mtime and hashes lazily (the git-style fast path). Irregular
// files (sockets, devices, FIFOs) are skipped: a userspace snapshot cannot
// faithfully restore them, and silently pretending otherwise would be
// worse than ignoring them.
func Tree(root string, ig *ignore.Matcher, hash bool) ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if ig.Match(rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		e, err := statEntry(p, rel, d, hash)
		if err != nil {
			return err
		}
		if e != nil {
			entries = append(entries, *e)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func statEntry(abs, rel string, d fs.DirEntry, hash bool) (*Entry, error) {
	info, err := d.Info()
	if err != nil {
		return nil, err
	}
	mode := info.Mode()
	switch {
	case mode.IsDir():
		return &Entry{Path: rel, Type: TypeDir, Mode: uint32(mode.Perm())}, nil
	case mode&fs.ModeSymlink != 0:
		target, err := os.Readlink(abs)
		if err != nil {
			return nil, err
		}
		return &Entry{Path: rel, Type: TypeSymlink, Target: target}, nil
	case mode.IsRegular():
		e := &Entry{
			Path:    rel,
			Type:    TypeFile,
			Mode:    uint32(mode.Perm()),
			Size:    info.Size(),
			MtimeNS: info.ModTime().UnixNano(),
		}
		if hash {
			h, err := HashFile(abs)
			if err != nil {
				return nil, err
			}
			e.Hash = h
		}
		return e, nil
	default:
		// Sockets, devices, FIFOs: skip (documented limitation).
		return nil, nil
	}
}

// HashFile streams a file through SHA-256 and returns the lowercase hex
// digest. Streaming keeps memory flat for large files.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ByPath indexes entries for O(1) lookup during change detection.
func ByPath(entries []Entry) map[string]Entry {
	m := make(map[string]Entry, len(entries))
	for _, e := range entries {
		m[e.Path] = e
	}
	return m
}
