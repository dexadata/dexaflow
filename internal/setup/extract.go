package setup

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Runner runs an external command; injected so venv provisioning is testable
// without a real Python.
type Runner func(ctx context.Context, name string, args ...string) error

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

// ProvisionVenv creates a virtualenv at venvDir using pythonPath, then pip
// installs pipPackages into it, and returns the venv's interpreter path. The
// heavy work (notably the parser's Airflow dependency) happens once; reruns are
// the caller's responsibility to gate. The runner is injected for testing.
func ProvisionVenv(ctx context.Context, run Runner, pythonPath, venvDir string, pipPackages []string) (string, error) {
	if err := run(ctx, pythonPath, "-m", "venv", venvDir); err != nil {
		return "", fmt.Errorf("creating venv at %s: %w", venvDir, err)
	}
	venvPy := filepath.Join(venvDir, "bin", "python")
	args := append([]string{"-m", "pip", "install", "-q"}, pipPackages...)
	if err := run(ctx, venvPy, args...); err != nil {
		return "", fmt.Errorf("installing packages into venv: %w", err)
	}
	return venvPy, nil
}
