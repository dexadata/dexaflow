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

// Pinned Monaco editor release served by the Lite web editor (ADR 0025). It is
// fetched once into the managed home and served from disk, so the binary stays
// light. The npm tarball is immutable and content-addressed.
const (
	monacoVersion = "0.52.2"
	monacoURL     = "https://registry.npmjs.org/monaco-editor/-/monaco-editor-" + monacoVersion + ".tgz"
	monacoSHA256  = "c280cdcf0b0c13d1a2bf01af958d4387ed06d7f6c918401d00c4adcae1bc72b6"

	// maxMonacoArchiveBytes caps the download (the real tarball is ~18 MB).
	maxMonacoArchiveBytes = 80 << 20 // 80 MiB

	// monacoVSPrefix is the path inside the npm tarball holding the minified
	// browser bundle; everything else (esm, dev, sourcemaps) is skipped.
	monacoVSPrefix = "package/min/"
)

// MonacoDir returns the directory the pinned Monaco bundle is served from
// (home/assets/monaco/<version>). The editor loads /ide/vs from <dir>/vs.
func MonacoDir(home string) string {
	return filepath.Join(home, "assets", "monaco", monacoVersion)
}

// EnsureMonaco makes the pinned Monaco bundle available under MonacoDir(home)
// and returns that directory. It is a no-op when already provisioned; otherwise
// it downloads the pinned npm tarball, verifies its SHA-256, and extracts only
// the min/vs subtree.
func EnsureMonaco(ctx context.Context, client *http.Client, home string, logf func(string, ...any)) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return ensureMonaco(ctx, client, home, monacoURL, monacoSHA256, logf)
}

// ensureMonaco is EnsureMonaco with the URL and checksum injected, so tests can
// drive it against a local server.
func ensureMonaco(ctx context.Context, client *http.Client, home, url, wantSHA string, logf func(string, ...any)) (string, error) {
	dir := MonacoDir(home)
	marker := filepath.Join(dir, "vs", "loader.js")
	if _, err := os.Stat(marker); err == nil {
		return dir, nil
	}
	logf2(logf, "downloading Monaco editor %s...", monacoVersion)
	data, err := download(ctx, client, url, wantSHA)
	if err != nil {
		return "", err
	}
	if err := extractMonacoVS(data, dir); err != nil {
		return "", err
	}
	logf2(logf, "Monaco editor installed at %s", dir)
	return dir, nil
}

// logf2 invokes the optional progress callback, ignoring a nil one.
func logf2(f func(string, ...any), format string, a ...any) {
	if f != nil {
		f(format, a...)
	}
}

// download fetches url, caps the body, and verifies its SHA-256 against wantSHA.
func download(ctx context.Context, client *http.Client, url, wantSHA string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading Monaco: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of the download body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading Monaco: unexpected status %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMonacoArchiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading Monaco archive: %w", err)
	}
	if len(data) > maxMonacoArchiveBytes {
		return nil, fmt.Errorf("monaco archive exceeds %d bytes", maxMonacoArchiveBytes)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("monaco checksum mismatch: got %s, want %s", got, wantSHA)
	}
	return data, nil
}

// extractMonacoVS unpacks only the min/vs subtree of the npm tarball into
// dest, stripping the "package/min/" prefix so files land at dest/vs/...,
// rejecting any entry that would escape dest (zip-slip).
func extractMonacoVS(data []byte, dest string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("opening gzip stream: %w", err)
	}
	defer func() { _ = gz.Close() }() //nolint:errcheck // best-effort close of the gzip reader

	clean := filepath.Clean(dest)
	tr := tar.NewReader(gz)
	for {
		hdr, terr := tr.Next()
		if terr == io.EOF {
			return nil
		}
		if terr != nil {
			return fmt.Errorf("reading tar entry: %w", terr)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasPrefix(hdr.Name, monacoVSPrefix+"vs/") {
			continue
		}
		rel := strings.TrimPrefix(hdr.Name, monacoVSPrefix) // -> "vs/..."
		target := filepath.Join(clean, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, clean+string(os.PathSeparator)) {
			return fmt.Errorf("tar entry %q escapes destination", hdr.Name)
		}
		if werr := writeMonacoFile(tr, target); werr != nil {
			return werr
		}
	}
}

// writeMonacoFile writes one regular tar entry to target, creating parents.
func writeMonacoFile(tr *tar.Reader, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("creating parent of %q: %w", target, err)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("creating %q: %w", target, err)
	}
	if _, err := io.Copy(f, io.LimitReader(tr, maxMonacoArchiveBytes)); err != nil { // bounded by the archive cap
		_ = f.Close() //nolint:errcheck // already returning an error
		return fmt.Errorf("writing %q: %w", target, err)
	}
	return f.Close()
}
