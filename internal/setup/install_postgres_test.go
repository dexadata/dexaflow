package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tarGz builds a gzipped tar from the given headers (Size/body taken from each
// header's paired body, "" for non-regular entries) for extraction tests.
func tarGz(t *testing.T, entries []*tar.Header, bodies []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for i, h := range entries {
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(bodies[i]))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(bodies[i])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestExtractTarGzStripRejectsZipSlip proves an archive entry that climbs out of
// the destination via ".." (even after the top-level strip) is refused and never
// written.
func TestExtractTarGzStripRejectsZipSlip(t *testing.T) {
	data := tarGz(t,
		[]*tar.Header{{Name: "../../evil.txt", Typeflag: tar.TypeReg, Mode: 0o644}},
		[]string{"pwned"})
	dest := t.TempDir()
	err := extractTarGzStrip(data, dest, 1)
	if err == nil || !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("err = %v, want an 'escapes destination' rejection", err)
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); !os.IsNotExist(statErr) {
		t.Error("zip-slip entry was written outside the destination")
	}
}

// TestExtractTarGzStripRejectsEscapingSymlink proves a symlink whose target
// resolves outside the destination is refused (so a later entry cannot be
// written through it to escape the extraction root).
func TestExtractTarGzStripRejectsEscapingSymlink(t *testing.T) {
	data := tarGz(t,
		[]*tar.Header{{Name: "pg/link", Linkname: "../../../../etc/passwd", Typeflag: tar.TypeSymlink, Mode: 0o777}},
		[]string{""})
	dest := t.TempDir()
	err := extractTarGzStrip(data, dest, 1)
	if err == nil || !strings.Contains(err.Error(), "escapes destination") {
		t.Fatalf("err = %v, want an 'escapes destination' rejection", err)
	}
}

// TestExtractTarGzStripAllowsInternalSymlink confirms a symlink whose target
// stays within the destination (the common lib/*.so.N → *.so case) is allowed.
func TestExtractTarGzStripAllowsInternalSymlink(t *testing.T) {
	data := tarGz(t,
		[]*tar.Header{
			{Name: "pg/lib/libpq.so.5", Typeflag: tar.TypeReg, Mode: 0o755},
			{Name: "pg/lib/libpq.so", Linkname: "libpq.so.5", Typeflag: tar.TypeSymlink, Mode: 0o777},
		},
		[]string{"SO", ""})
	dest := t.TempDir()
	if err := extractTarGzStrip(data, dest, 1); err != nil {
		t.Fatalf("extractTarGzStrip rejected a valid internal symlink: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "lib", "libpq.so")); err != nil {
		t.Errorf("internal symlink was not created: %v", err)
	}
}

// errRoundTripper fails any HTTP request, proving a code path made no network call.
type errRoundTripper struct{ t *testing.T }

func (e errRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	e.t.Error("unexpected network call")
	return nil, errors.New("no network")
}

// recordingRoundTripper records that a request was made (without failing the
// test) and returns an error, so a test can assert a code path DID reach out.
type recordingRoundTripper struct{ called bool }

func (r *recordingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	r.called = true
	return nil, errors.New("network blocked")
}

// TestExtractSymlinkIsIdempotent proves a re-extract over an existing install
// does not fail on a pre-existing symlink entry (#729: os.Symlink returns EEXIST
// when the target already exists, unlike the O_TRUNC regular-file branch).
func TestExtractSymlinkIsIdempotent(t *testing.T) {
	data := tarGz(t,
		[]*tar.Header{
			{Name: "pg/lib/libpq.so.5", Typeflag: tar.TypeReg, Mode: 0o755},
			{Name: "pg/lib/libpq.so", Linkname: "libpq.so.5", Typeflag: tar.TypeSymlink, Mode: 0o777},
		},
		[]string{"SO", ""})
	dest := t.TempDir()
	if err := extractTarGzStrip(data, dest, 1); err != nil {
		t.Fatalf("first extract: %v", err)
	}
	if err := extractTarGzStrip(data, dest, 1); err != nil {
		t.Fatalf("second extract over existing install: %v", err)
	}
	link, err := os.Readlink(filepath.Join(dest, "lib", "libpq.so"))
	if err != nil || link != "libpq.so.5" {
		t.Errorf("symlink = %q err = %v, want -> libpq.so.5", link, err)
	}
}

func TestEnsurePostgresReturnsExistingManaged(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "postgres", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "postgres"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A managed Postgres already present AT THE BUNDLED VERSION must short-circuit
	// before any download.
	if err := os.WriteFile(filepath.Join(home, "postgres", pgVersionFile), []byte(pgVersion), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnsurePostgres(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "arm64",
		Client: &http.Client{Transport: errRoundTripper{t}},
	})
	if err != nil {
		t.Fatalf("EnsurePostgres: %v", err)
	}
	if got != binDir {
		t.Errorf("bin dir = %q, want %q", got, binDir)
	}
}

// TestEnsurePostgresReextractsOnVersionChange proves the presence guard is
// version-aware: when the managed Postgres on disk records a DIFFERENT version
// than the bundled one, EnsurePostgres re-installs (reaches the download) instead
// of silently keeping the stale release. Without a version sentinel it would
// short-circuit forever, so a clean upgrade would never re-extract.
func TestEnsurePostgresReextractsOnVersionChange(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "postgres", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "postgres"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Sentinel records an OLDER managed version than the bundled pgVersion.
	if err := os.WriteFile(filepath.Join(home, "postgres", pgVersionFile), []byte("15.0.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := &recordingRoundTripper{}
	_, err := EnsurePostgres(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "arm64",
		Client: &http.Client{Transport: rt},
	})
	if err == nil {
		t.Fatal("EnsurePostgres: err = nil, want a download attempt on version change")
	}
	if !rt.called {
		t.Error("expected a re-download when the on-disk PG version differs from the bundled one")
	}
}

// TestEnsurePostgresReextractsWhenVersionUnknown proves a managed install with no
// version sentinel (a pre-#729 install, or an interrupted extract) is treated as
// needing re-installation rather than trusted blindly.
func TestEnsurePostgresReextractsWhenVersionUnknown(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "postgres", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "postgres"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := &recordingRoundTripper{}
	_, err := EnsurePostgres(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "arm64",
		Client: &http.Client{Transport: rt},
	})
	if err == nil {
		t.Fatal("EnsurePostgres: err = nil, want a download attempt when no version is recorded")
	}
	if !rt.called {
		t.Error("expected a re-download when no version sentinel is present")
	}
}

// TestEnsurePostgresFailedDownloadKeepsExistingInstall locks the crash-safety
// invariant of #754 for the Postgres entry point: EnsurePostgres runs fetchVerify
// (download + checksum) BEFORE extractTarGzStrip touches the managed tree, so a
// download that errors must leave a working managed Postgres on disk rather than
// wiping it. Setup is the upgrade case: a complete install of an OLDER version is
// present (its sentinel forces EnsurePostgres into the download), the download
// fails, and the pre-existing binary must survive.
func TestEnsurePostgresFailedDownloadKeepsExistingInstall(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "postgres", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(binDir, "postgres")
	if err := os.WriteFile(managed, []byte("#!/existing postgres\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Sentinel records an OLDER version, so EnsurePostgres reaches the download.
	if err := os.WriteFile(filepath.Join(home, "postgres", pgVersionFile), []byte("15.0.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := &recordingRoundTripper{}
	_, err := EnsurePostgres(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "arm64",
		Client: &http.Client{Transport: rt},
	})
	if err == nil {
		t.Fatal("EnsurePostgres: err = nil, want the failed download surfaced as an error")
	}
	if !rt.called {
		t.Fatal("expected EnsurePostgres to attempt the download")
	}
	got, rerr := os.ReadFile(managed)
	if rerr != nil {
		t.Fatalf("existing managed postgres was destroyed by a failed download: %v", rerr)
	}
	if string(got) != "#!/existing postgres\n" {
		t.Errorf("managed postgres = %q, want the pre-existing content intact", got)
	}
}

func TestStripComponents(t *testing.T) {
	cases := map[string]string{
		"postgresql-16.13.0-x/bin/postgres": filepath.Join("bin", "postgres"),
		"postgresql-16.13.0-x/lib/a.so":     filepath.Join("lib", "a.so"),
		"top/":                              "", // the stripped top-level dir itself
		"top":                               "",
	}
	for in, want := range cases {
		if got := stripComponents(in, 1); got != want {
			t.Errorf("stripComponents(%q, 1) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractTarGzStripFlattensTopDir(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "pg-16/", Typeflag: tar.TypeDir, Mode: 0o755})
	for name, body := range map[string]string{"pg-16/bin/postgres": "BIN", "pg-16/share/x.sql": "SQL"} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()

	dest := t.TempDir()
	if err := extractTarGzStrip(buf.Bytes(), dest, 1); err != nil {
		t.Fatalf("extractTarGzStrip: %v", err)
	}
	// The top-level pg-16/ is stripped: bin/postgres lands directly under dest.
	if b, err := os.ReadFile(filepath.Join(dest, "bin", "postgres")); err != nil || string(b) != "BIN" {
		t.Errorf("bin/postgres = %q (err %v), want BIN", b, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "pg-16")); !os.IsNotExist(err) {
		t.Error("the top-level archive dir must be stripped, not present under dest")
	}
}

// TestExtractTarGzStripHardlink covers the hard links of the self-contained
// PostgreSQL build (its duplicate time zone files, #2540): the link names its
// source by the full archive path, which is stripped like the entry name, and
// the entry becomes a copy of that file.
func TestExtractTarGzStripHardlink(t *testing.T) {
	data := tarGz(t,
		[]*tar.Header{
			{Name: "pg-16/share/timezone/America/Santa_Isabel", Typeflag: tar.TypeReg, Mode: 0o644},
			{Name: "pg-16/share/timezone/America/Tijuana", Linkname: "pg-16/share/timezone/America/Santa_Isabel", Typeflag: tar.TypeLink, Mode: 0o644},
			{Name: "pg-16/bin/postgres", Typeflag: tar.TypeReg, Mode: 0o755},
			{Name: "pg-16/bin/postmaster", Linkname: "pg-16/bin/postgres", Typeflag: tar.TypeLink, Mode: 0o755},
		},
		[]string{"TZif", "", "ELF", ""})
	dest := t.TempDir()
	for range 2 { // a re-extract over the first one works too
		if err := extractTarGzStrip(data, dest, 1); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(dest, "share", "timezone", "America", "Tijuana"))
	if err != nil || string(b) != "TZif" {
		t.Fatalf("hard-linked file = %q, %v; want the source's content", b, err)
	}
	fi, err := os.Stat(filepath.Join(dest, "bin", "postmaster"))
	if err != nil || fi.Mode()&0o100 == 0 {
		t.Fatalf("hard link to an executable: %v, mode %v; want it executable", err, fi)
	}
}

// TestExtractTarGzStripRejectsBadHardlink proves a hard link can only copy a
// regular file the archive already extracted inside the destination.
func TestExtractTarGzStripRejectsBadHardlink(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]*tar.Header{
		"escaping": {{Name: "pg/x", Linkname: "pg/../../../etc/passwd", Typeflag: tar.TypeLink}},
		"absolute": {{Name: "pg/x", Linkname: outside, Typeflag: tar.TypeLink}},
		"missing":  {{Name: "pg/x", Linkname: "pg/later", Typeflag: tar.TypeLink}},
		"via a symlink": {
			{Name: "pg/dir", Typeflag: tar.TypeDir, Mode: 0o755},
			{Name: "pg/dir/sl", Linkname: "../dir", Typeflag: tar.TypeSymlink},
			{Name: "pg/x", Linkname: "pg/dir/sl", Typeflag: tar.TypeLink},
		},
		"to the top directory": {{Name: "pg/x", Linkname: "pg", Typeflag: tar.TypeLink}},
	}
	for name, hdrs := range cases {
		t.Run(name, func(t *testing.T) {
			dest := t.TempDir()
			if err := extractTarGzStrip(tarGz(t, hdrs, make([]string, len(hdrs))), dest, 1); err == nil {
				t.Fatal("want the hard link refused")
			}
			if _, err := os.Stat(filepath.Join(dest, "x")); !os.IsNotExist(err) {
				t.Errorf("the refused link was written: %v", err)
			}
		})
	}
}
