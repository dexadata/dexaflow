// Package workspace provides a filesystem confined to a single root directory.
// Every operation resolves the caller-supplied path against the root and refuses
// any path that would escape it, so it is safe to drive from an HTTP API. It
// backs the Dexaflow Lite web editor (ADR 0025); it is never the Production path.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ErrUnsafePath is returned (wrapped) when a requested path is absolute or would
// escape the workspace root. Callers can map it to a client error with
// errors.Is.
var ErrUnsafePath = errors.New("unsafe path")

// noiseDirs are directory names skipped when building the tree: version-control
// and tooling caches that only clutter the editor's file list.
var noiseDirs = map[string]bool{
	".git":          true,
	".hg":           true,
	"node_modules":  true,
	"__pycache__":   true,
	".venv":         true,
	".mypy_cache":   true,
	".ruff_cache":   true,
	".pytest_cache": true,
}

// FS is a filesystem confined to a single root directory. The zero value is not
// usable; construct one with New.
type FS struct {
	root string // absolute, cleaned, symlink-resolved
}

// Entry is one node in the workspace tree. Path is relative to the root and
// always slash-separated.
type Entry struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// New returns an FS rooted at dir. dir must be an existing directory; New
// resolves it to an absolute, symlink-free path so later confinement checks are
// exact.
func New(dir string) (*FS, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolving workspace root: %w", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("workspace root %q is not a directory", dir)
	}
	return &FS{root: resolved}, nil
}

// Root returns the absolute, resolved root directory.
func (f *FS) Root() string { return f.root }

// resolve maps a slash-separated relative path to an absolute path inside the
// root, refusing absolute inputs and any path that escapes the root via "..".
//
// The first gate is the stdlib's filepath.IsLocal — it rejects absolute paths,
// empty inputs, paths that climb out via "..", and (on Windows) reserved
// device names. Using IsLocal here also lets static analyzers (CodeQL's
// go/path-injection rule, gosec's G304) recognize this function as the
// sanitizer for caller paths, so downstream os.* calls do not need
// per-call suppressions.
func (f *FS) resolve(rel string) (string, error) {
	slashed := filepath.ToSlash(rel)
	if !filepath.IsLocal(slashed) {
		return "", fmt.Errorf("%w: %q is not a local workspace path", ErrUnsafePath, rel)
	}
	clean := path.Clean(slashed)
	if clean == "." {
		return "", fmt.Errorf("%w: %q resolves to the workspace root", ErrUnsafePath, rel)
	}
	abs := filepath.Join(f.root, filepath.FromSlash(clean))
	// Defense in depth: even after IsLocal, re-check that the joined absolute
	// path stays under the root. Cheap and catches future Go releases or OS
	// edge cases where the lexical guarantees of IsLocal might drift.
	if abs != f.root && !strings.HasPrefix(abs, f.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q escapes the workspace", ErrUnsafePath, rel)
	}
	return abs, nil
}

// Read returns the contents of the file at rel.
func (f *FS) Read(rel string) ([]byte, error) {
	abs, err := f.resolve(rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs) //nolint:gosec // abs is confined to the root by resolve
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", rel, err)
	}
	return data, nil
}

// Write saves data to the file at rel, creating any missing parent directories.
func (f *FS) Write(rel string, data []byte) error {
	abs, err := f.resolve(rel)
	if err != nil {
		return err
	}
	if mkErr := os.MkdirAll(filepath.Dir(abs), 0o750); mkErr != nil {
		return fmt.Errorf("creating parent of %q: %w", rel, mkErr)
	}
	if wErr := os.WriteFile(abs, data, 0o600); wErr != nil {
		return fmt.Errorf("writing %q: %w", rel, wErr)
	}
	return nil
}

// Create makes an empty file at rel, or a directory when dir is true, including
// any missing parents.
func (f *FS) Create(rel string, dir bool) error {
	abs, err := f.resolve(rel)
	if err != nil {
		return err
	}
	if dir {
		if mkErr := os.MkdirAll(abs, 0o750); mkErr != nil {
			return fmt.Errorf("creating directory %q: %w", rel, mkErr)
		}
		return nil
	}
	if mkErr := os.MkdirAll(filepath.Dir(abs), 0o750); mkErr != nil {
		return fmt.Errorf("creating parent of %q: %w", rel, mkErr)
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600) //nolint:gosec // abs confined by resolve
	if err != nil {
		return fmt.Errorf("creating %q: %w", rel, err)
	}
	return file.Close()
}

// Move renames or relocates a file or directory inside the workspace. It
// auto-creates any missing parent directories on the destination side, and
// refuses to clobber an existing destination — the IDE surface (drag-drop /
// rename) asks the user to pick a fresh name on a collision.
func (f *FS) Move(from, to string) error {
	src, err := f.resolve(from)
	if err != nil {
		return err
	}
	dst, err := f.resolve(to)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("destination %q already exists", to)
	}
	if mkErr := os.MkdirAll(filepath.Dir(dst), 0o750); mkErr != nil {
		return fmt.Errorf("creating parent of %q: %w", to, mkErr)
	}
	if rnErr := os.Rename(src, dst); rnErr != nil {
		return fmt.Errorf("moving %q to %q: %w", from, to, rnErr)
	}
	return nil
}

// Delete removes the file or directory (recursively) at rel.
func (f *FS) Delete(rel string) error {
	abs, err := f.resolve(rel)
	if err != nil {
		return err
	}
	if rmErr := os.RemoveAll(abs); rmErr != nil {
		return fmt.Errorf("deleting %q: %w", rel, rmErr)
	}
	return nil
}

// Tree walks the workspace and returns every file and directory (except noise
// directories), with paths relative to the root, sorted for a stable display.
func (f *FS) Tree() ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(f.root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == f.root {
			return nil
		}
		if d.IsDir() && noiseDirs[d.Name()] {
			return filepath.SkipDir
		}
		rel, relErr := filepath.Rel(f.root, p)
		if relErr != nil {
			return relErr
		}
		var size int64
		if info, infoErr := d.Info(); infoErr == nil {
			size = info.Size()
		}
		entries = append(entries, Entry{
			Path:  filepath.ToSlash(rel),
			Name:  d.Name(),
			IsDir: d.IsDir(),
			Size:  size,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking workspace: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}
