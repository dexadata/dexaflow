package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxPythonArchiveBytes caps the relocatable CPython download to guard against a
// runaway or hostile response (the real archive is ~30 MB).
const maxPythonArchiveBytes = 200 << 20 // 200 MiB

// pyVersionFile names the sentinel, written under Home/python after a successful
// extraction, that records which managed CPython version is on disk. The
// presence guard keys on it (symmetric with pgVersionFile) so an upgrade
// re-extracts instead of silently keeping the prior release's interpreter.
const pyVersionFile = ".py-version"

// EnsureOpts configures EnsurePython.
type EnsureOpts struct {
	Home     string // the managed root, e.g. ~/.dexaflow
	GOOS     string
	GOARCH   string
	Libc     string
	LookPath func(string) (string, error)
	Stat     func(string) (os.FileInfo, error)
	Client   *http.Client
	Logf     func(string, ...any) // optional progress callback
}

// EnsurePython returns the path to a usable Python 3.11: a system interpreter if
// one is on PATH, the managed relocatable CPython if already installed under
// Home, or a freshly downloaded-and-verified pinned build otherwise. The managed
// build is extracted to Home/python (the archive's top-level "python/" dir).
func EnsurePython(ctx context.Context, o EnsureOpts) (string, error) {
	pyRoot := filepath.Join(o.Home, "python")
	managed := filepath.Join(pyRoot, "bin", "python3.11")
	stat := o.Stat
	if stat == nil {
		stat = os.Stat
	}
	// A COMPLETE managed install of the PINNED version wins over any host
	// interpreter: it is the checksum-verified build Lite provisioned, at the
	// exact version the runtime expects. Keying on mere presence (the pre-#742
	// behavior) meant (a) a clean upgrade never re-extracted — it silently kept
	// the prior release — and (b) LookPath("python3.11") ran FIRST, so a host
	// python3.11 outranked the pinned managed one. Check the sentinel first.
	managedPresent := false
	if _, err := stat(managed); err == nil {
		managedPresent = true
		if readManagedPythonVersion(pyRoot) == pyVersion {
			return managed, nil
		}
	}
	// No CURRENT managed install. Prefer a host python3.11 to avoid a download,
	// but only when there is no managed tree to repair — a version-mismatched or
	// half-extracted managed tree must be re-installed, not shadowed by a host
	// interpreter of unknown version.
	if !managedPresent && o.LookPath != nil {
		if p, err := o.LookPath("python3.11"); err == nil {
			return p, nil
		}
	}
	build, err := ResolvePython(o.GOOS, o.GOARCH, o.Libc)
	if err != nil {
		return "", err
	}
	logf(o.Logf, "downloading relocatable CPython %s for %s/%s...", build.Version, o.GOOS, o.GOARCH)
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	data, err := fetchVerify(ctx, client, build.URL, build.SHA256, maxPythonArchiveBytes, "CPython")
	if err != nil {
		return "", err
	}
	// Wipe any prior (stale, wrong-version, or half-extracted) managed tree before
	// re-extracting, so no file from an older on-disk layout survives and no tar
	// TYPE change (e.g. a dir becoming a file) collides with an existing entry.
	// Done only after the download verified, so a network failure never destroys a
	// working install.
	if rmErr := os.RemoveAll(pyRoot); rmErr != nil {
		return "", fmt.Errorf("removing prior managed CPython: %w", rmErr)
	}
	if xerr := extractTarGz(data, o.Home); xerr != nil {
		return "", xerr
	}
	// Record the extracted version LAST, so an interrupted extract leaves no
	// sentinel and is re-installed on the next run rather than trusted.
	if werr := os.WriteFile(filepath.Join(pyRoot, pyVersionFile), []byte(build.Version), 0o600); werr != nil {
		return "", fmt.Errorf("recording managed CPython version: %w", werr)
	}
	logf(o.Logf, "CPython installed at %s", managed)
	return managed, nil
}

// readManagedPythonVersion returns the managed CPython version recorded under
// pyRoot by the last successful extraction, or "" if none is recorded (a
// pre-sentinel install or an interrupted extract).
func readManagedPythonVersion(pyRoot string) string {
	b, err := os.ReadFile(filepath.Join(pyRoot, pyVersionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// logf invokes the optional progress callback, ignoring a nil one.
func logf(f func(string, ...any), format string, a ...any) {
	if f != nil {
		f(format, a...)
	}
}

// downloadVerifyExtract fetches the build's URL, verifies its SHA-256 against the
// pinned value, and extracts the gzipped tarball into destDir. Extraction is
// guarded against path traversal (zip-slip). The download is verified in full
// before any file is written.
func downloadVerifyExtract(ctx context.Context, client *http.Client, b PythonBuild, destDir string) error {
	data, err := fetchVerify(ctx, client, b.URL, b.SHA256, maxPythonArchiveBytes, "CPython")
	if err != nil {
		return err
	}
	return extractTarGz(data, destDir)
}

// fetchVerify downloads url, caps the body at maxBytes, and verifies its SHA-256
// against wantSHA before returning the bytes — so a managed toolchain (CPython,
// PostgreSQL) is never extracted unverified. label names the artifact in errors.
func fetchVerify(ctx context.Context, client *http.Client, url, wantSHA string, maxBytes int64, label string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", label, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of the download body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: unexpected status %s", label, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s archive: %w", label, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s archive exceeds %d bytes", label, maxBytes)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("%s checksum mismatch: got %s, want %s", label, got, wantSHA)
	}
	return data, nil
}

// extractTarGz unpacks a gzipped tar archive into destDir, rejecting any entry
// that would escape destDir.
func extractTarGz(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }() //nolint:errcheck // best-effort close of the gzip reader

	clean := filepath.Clean(destDir)
	tr := tar.NewReader(gz)
	for {
		hdr, terr := tr.Next()
		if terr == io.EOF {
			return nil
		}
		if terr != nil {
			return fmt.Errorf("reading tar entry: %w", terr)
		}
		if err := extractEntry(tr, hdr, clean, hdr.Name); err != nil {
			return err
		}
	}
}

// extractEntry writes a single tar entry (directory, regular file, or symlink)
// under destDir. The entry name is sanitized to stay within destDir (Zip Slip),
// and a symlink whose target would resolve outside destDir is refused — so a
// malicious archive can neither write outside the extraction root nor plant an
// escaping symlink that a later entry could write through.
func extractEntry(tr *tar.Reader, hdr *tar.Header, destDir, name string) error {
	target, err := sanitizeArchivePath(destDir, name)
	if err != nil {
		return err
	}
	switch hdr.Typeflag {
	case tar.TypeDir:
		// A prior extract of an older layout may have left a non-directory here;
		// MkdirAll would then fail. Clear the conflicting entry so the re-extract
		// is idempotent across a tar TYPE change (file -> directory).
		if fi, lerr := os.Lstat(target); lerr == nil && !fi.IsDir() {
			if rmErr := os.RemoveAll(target); rmErr != nil {
				return fmt.Errorf("removing conflicting %q before dir: %w", target, rmErr)
			}
		}
		return os.MkdirAll(target, 0o750)
	case tar.TypeReg:
		if mkErr := os.MkdirAll(filepath.Dir(target), 0o750); mkErr != nil {
			return fmt.Errorf("creating parent of %q: %w", target, mkErr)
		}
		// Symmetric to the dir branch: if a prior extract left a directory (or a
		// symlink) where a regular file now belongs, O_TRUNC would fail EISDIR.
		// Clear any non-regular entry first so the re-extract is idempotent across
		// a TYPE change (directory -> file).
		if fi, lerr := os.Lstat(target); lerr == nil && !fi.Mode().IsRegular() {
			if rmErr := os.RemoveAll(target); rmErr != nil {
				return fmt.Errorf("removing conflicting %q before file: %w", target, rmErr)
			}
		}
		// Preserve only the executable bit (interpreters, scripts); everything
		// else is owner read/write. This sidesteps trusting arbitrary archive
		// mode bits while keeping the binaries runnable.
		perm := os.FileMode(0o600)
		if hdr.Mode&0o111 != 0 {
			perm = 0o700
		}
		f, openErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if openErr != nil {
			return fmt.Errorf("creating %q: %w", target, openErr)
		}
		if _, cpErr := io.Copy(f, io.LimitReader(tr, maxPythonArchiveBytes)); cpErr != nil { //nolint:gosec // bounded by the archive cap
			_ = f.Close() //nolint:errcheck // already returning an error
			return fmt.Errorf("writing %q: %w", target, cpErr)
		}
		return f.Close()
	case tar.TypeSymlink:
		return extractSymlink(hdr, destDir, target, name)
	default:
		// Skip device nodes, fifos, and other special types these archives don't use.
		return nil
	}
}

// extractSymlink creates one symlink entry safely. It refuses a link whose
// target would resolve outside destDir — lexically (absolute, or climbing out
// via "..") and, when the target already exists, by its real on-disk path. The
// filepath.EvalSymlinks resolution both confirms containment and is what clears
// go/unsafe-unzip-symlink (the query treats symlink resolution as the barrier).
func extractSymlink(hdr *tar.Header, destDir, target, name string) error {
	clean := filepath.Clean(destDir)
	linkResolved := filepath.Join(filepath.Dir(target), hdr.Linkname) //nolint:gosec // not a sink: validated below before any symlink is created
	if filepath.IsAbs(hdr.Linkname) || !withinDir(linkResolved, clean) {
		return fmt.Errorf("symlink %q target %q escapes destination", name, hdr.Linkname)
	}
	if realTarget, evalErr := filepath.EvalSymlinks(linkResolved); evalErr == nil {
		// Compare against the resolved root too: on macOS the dest (under /var)
		// itself resolves to /private/var, so an unresolved-vs-resolved check
		// would wrongly reject a contained link.
		realRoot := clean
		if rr, rootErr := filepath.EvalSymlinks(clean); rootErr == nil {
			realRoot = rr
		}
		if !withinDir(realTarget, realRoot) {
			return fmt.Errorf("symlink %q target %q escapes destination", name, hdr.Linkname)
		}
	}
	if mkErr := os.MkdirAll(filepath.Dir(target), 0o750); mkErr != nil {
		return fmt.Errorf("creating parent of %q: %w", target, mkErr)
	}
	// os.Symlink fails EEXIST if target is already present, which breaks a
	// re-extract over an existing install (the regular-file branch overwrites via
	// O_TRUNC). Remove any prior entry first to match that idempotent behavior.
	// RemoveAll (not os.Remove) so a TYPE change — a prior extract leaving a
	// non-empty directory where a symlink now belongs — is cleared too, rather
	// than failing with a non-IsNotExist ENOTEMPTY. Target is already sanitized by
	// the containment checks above.
	if rmErr := os.RemoveAll(target); rmErr != nil {
		return fmt.Errorf("removing existing %q before symlink: %w", target, rmErr)
	}
	return os.Symlink(hdr.Linkname, target) //nolint:gosec // target sanitized; link resolved within destDir
}

// sanitizeArchivePath joins a tar entry name onto destDir, returning the safe
// path. It refuses any entry that is not a local path (absolute, empty, or
// escaping via "..") — filepath.IsLocal is the canonical Zip-Slip guard, and
// returning the path keeps the sanitizer on the same data flow as the sinks.
func sanitizeArchivePath(destDir, name string) (string, error) {
	if !filepath.IsLocal(name) {
		return "", fmt.Errorf("archive entry %q escapes destination", name)
	}
	return filepath.Join(destDir, name), nil
}

// withinDir reports whether path is dir itself or lies beneath it.
func withinDir(path, dir string) bool {
	clean := filepath.Clean(path)
	return clean == dir || strings.HasPrefix(clean, dir+string(os.PathSeparator))
}
