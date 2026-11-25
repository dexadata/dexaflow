package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// maxPostgresArchiveBytes caps the relocatable PostgreSQL download. The real
// archive is ~11 MB (~38 MB for the self-contained build); the bound guards against a runaway/hostile response.
const maxPostgresArchiveBytes = 150 << 20 // 150 MiB

// pgVersionFile names the sentinel, written under Home/postgres after a
// successful extraction, that records which managed PostgreSQL version is on
// disk. The presence guard keys on it so an upgrade re-extracts instead of
// silently keeping the prior release's binaries.
const pgVersionFile = ".pg-version"

// EnsurePostgres returns the bin directory of a managed relocatable PostgreSQL
// (Home/postgres/bin), downloading + verifying + extracting the pinned build if
// it is not already present. Unlike Python there is NO system fallback: Lite
// manages its own Postgres so the version and on-disk layout are predictable and
// — the point of Fase 2 — it needs no Docker. The data directory and process
// lifecycle are the caller's concern (see the Lite runner).
func EnsurePostgres(ctx context.Context, o EnsureOpts) (string, error) {
	pgRoot := filepath.Join(o.Home, "postgres")
	binDir := filepath.Join(pgRoot, "bin")
	managed := filepath.Join(binDir, "postgres")
	stat := o.Stat
	if stat == nil {
		stat = os.Stat
	}
	build, err := ResolvePostgres(o.GOOS, o.GOARCH, o.Libc, o.Glibc)
	if err != nil {
		return "", err
	}
	// Short-circuit only when a COMPLETE install of the build this host gets is
	// present. Keying on presence alone (the pre-#729 behavior) meant a clean
	// upgrade never re-extracted (it silently kept the prior release's Postgres),
	// and a partial extract (interrupted, or an older on-disk layout) was trusted
	// blindly. Moving between the linked and the self-contained build re-extracts
	// the same way; both are PostgreSQL 16, so the data directory carries over.
	if _, serr := stat(managed); serr == nil && readManagedPostgresVersion(pgRoot) == build.Version {
		return binDir, nil
	}
	logf(o.Logf, "downloading relocatable PostgreSQL %s for %s/%s...", build.Version, o.GOOS, o.GOARCH)
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	data, err := fetchVerify(ctx, client, build.URL, build.SHA256, maxPostgresArchiveBytes, "PostgreSQL")
	if err != nil {
		return "", err
	}
	// theseus-rs nests everything under a single postgresql-<version>-<triple>/
	// directory; strip it so bin/postgres lands at Home/postgres/bin/postgres
	// regardless of the triple.
	if err := extractTarGzStrip(data, pgRoot, 1); err != nil {
		return "", err
	}
	// Record the extracted version last, so an interrupted extract leaves no
	// sentinel and is re-installed on the next run rather than trusted.
	if err := os.WriteFile(filepath.Join(pgRoot, pgVersionFile), []byte(build.Version), 0o600); err != nil {
		return "", fmt.Errorf("recording managed PostgreSQL version: %w", err)
	}
	logf(o.Logf, "PostgreSQL installed at %s", binDir)
	return binDir, nil
}

// readManagedPostgresVersion returns the managed PostgreSQL version recorded
// under pgRoot by the last successful extraction, or "" if none is recorded
// (a pre-sentinel install or an interrupted extract).
func readManagedPostgresVersion(pgRoot string) string {
	b, err := os.ReadFile(filepath.Join(pgRoot, pgVersionFile))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// extractTarGzStrip unpacks a gzipped tar into destDir, dropping the first strip
// leading path components from each entry (so a single top-level archive dir is
// flattened away), and rejecting any entry that would escape destDir.
func extractTarGzStrip(data []byte, destDir string, strip int) error {
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
		name := stripComponents(hdr.Name, strip)
		if name == "" {
			continue // the stripped top-level dir itself
		}
		if hdr.Typeflag == tar.TypeLink {
			// A hard link names its source by its full archive path; strip it the
			// same way so it resolves under destDir.
			if hdr.Linkname = stripComponents(hdr.Linkname, strip); hdr.Linkname == "" {
				return fmt.Errorf("hard link %q names the stripped top-level directory", hdr.Name)
			}
		}
		if err := extractEntry(tr, hdr, clean, name); err != nil {
			return err
		}
	}
}

// stripComponents drops the first n leading path components of a slash- or
// OS-separated archive name, returning "" when nothing remains.
func stripComponents(name string, n int) string {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(name)), "/")
	if len(parts) <= n {
		return ""
	}
	return filepath.Join(parts[n:]...)
}
