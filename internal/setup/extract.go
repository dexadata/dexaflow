package setup

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ExtractFS writes every file in fsys into dest, preserving the directory
// structure. It is used to materialize the embedded Python sources into
// ~/.dexaflow so a binary-only install can pip-install them.
func ExtractFS(fsys fs.FS, dest string) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o750); mkErr != nil {
			return fmt.Errorf("creating parent of %q: %w", target, mkErr)
		}
		data, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return fmt.Errorf("reading embedded %q: %w", p, rerr)
		}
		if werr := os.WriteFile(target, data, 0o600); werr != nil {
			return fmt.Errorf("writing %q: %w", target, werr)
		}
		return nil
	})
}
