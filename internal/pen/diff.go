// Diff rendering: turn a change list into a reviewable unified diff.
// Old file bodies come from the content-addressed store; new bodies from
// the live tree. Binary files, symlinks, mode flips, and type changes get
// one-line notices instead of hunks.
package pen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/JaydenCJ/cowpen/internal/difftext"
	"github.com/JaydenCJ/cowpen/internal/scan"
)

// Diff renders the unified diff of every change in p, optionally filtered
// to the given path prefixes (slash-separated, relative to the root).
// It returns "" when nothing changed.
func (w *Workspace) Diff(p *Pen, paths []string) (string, error) {
	changes, err := w.Status(p, false)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range changes {
		if !pathSelected(c.Path, paths) {
			continue
		}
		if err := w.renderChange(&sb, c); err != nil {
			return "", err
		}
	}
	return sb.String(), nil
}

// pathSelected reports whether path matches any filter (exact file or
// directory prefix). An empty filter selects everything.
func pathSelected(path string, filters []string) bool {
	if len(filters) == 0 {
		return true
	}
	for _, f := range filters {
		f = strings.Trim(strings.ReplaceAll(f, string(os.PathSeparator), "/"), "/")
		if f == "" || f == "." || path == f || strings.HasPrefix(path, f+"/") {
			return true
		}
	}
	return false
}

func (w *Workspace) renderChange(sb *strings.Builder, c Change) error {
	switch c.Kind {
	case KindMode:
		fmt.Fprintf(sb, "mode change %04o -> %04o: %s\n", c.Old.Mode, c.New.Mode, c.Path)
		return nil
	case KindType:
		fmt.Fprintf(sb, "type change %s -> %s: %s\n", c.Old.Type, c.New.Type, c.Path)
		return nil
	}

	oldIsFile := c.Old != nil && c.Old.Type == scan.TypeFile
	newIsFile := c.New != nil && c.New.Type == scan.TypeFile

	// Symlink edits are shown as target changes, not content hunks.
	if c.Old != nil && c.Old.Type == scan.TypeSymlink && c.Kind == KindModified {
		fmt.Fprintf(sb, "symlink change %s: %s -> %s\n", c.Path, c.Old.Target, c.New.Target)
		return nil
	}
	// Added/deleted dirs and symlinks: one-line notice.
	if !oldIsFile && !newIsFile {
		switch c.Kind {
		case KindAdded:
			fmt.Fprintf(sb, "added %s: %s\n", c.New.Type, c.Path)
		case KindDeleted:
			fmt.Fprintf(sb, "deleted %s: %s\n", c.Old.Type, c.Path)
		}
		return nil
	}

	var oldBody, newBody []byte
	var err error
	if oldIsFile {
		oldBody, err = w.store.ReadAll(c.Old.Hash)
		if err != nil {
			return err
		}
	}
	if newIsFile {
		newBody, err = os.ReadFile(filepath.Join(w.Root, filepath.FromSlash(c.Path)))
		if err != nil {
			return err
		}
	}
	aName, bName := "a/"+c.Path, "b/"+c.Path
	if !oldIsFile {
		aName = "/dev/null"
	}
	if !newIsFile {
		bName = "/dev/null"
	}
	if difftext.IsBinary(oldBody) || difftext.IsBinary(newBody) {
		fmt.Fprintf(sb, "Binary files %s and %s differ\n", aName, bName)
		return nil
	}
	sb.WriteString(difftext.Unified(aName, bName, oldBody, newBody, difftext.DefaultContext))
	return nil
}
