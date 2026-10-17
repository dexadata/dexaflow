package setup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz builds an in-memory .tar.gz from name->content entries and returns
// the bytes plus their hex SHA-256.
func makeTarGz(t *testing.T, entries map[string]string) (archive []byte, sha string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func serve(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadVerifyExtract(t *testing.T) {
	t.Run("good checksum extracts files", func(t *testing.T) {
		body, sum := makeTarGz(t, map[string]string{
			"python/bin/python3.11": "#!/fake interpreter",
			"python/lib/marker":     "x",
		})
		srv := serve(t, body)
		dest := t.TempDir()

		err := downloadVerifyExtract(context.Background(), srv.Client(),
			PythonBuild{URL: srv.URL, SHA256: sum}, dest)
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		got, rerr := os.ReadFile(filepath.Join(dest, "python", "bin", "python3.11"))
		if rerr != nil {
			t.Fatalf("reading extracted file: %v", rerr)
		}
		if string(got) != "#!/fake interpreter" {
			t.Errorf("content = %q, want the interpreter stub", got)
		}
	})

	t.Run("checksum mismatch is rejected", func(t *testing.T) {
		body, _ := makeTarGz(t, map[string]string{"python/x": "y"})
		srv := serve(t, body)
		dest := t.TempDir()

		err := downloadVerifyExtract(context.Background(), srv.Client(),
			PythonBuild{URL: srv.URL, SHA256: "deadbeef"}, dest)
		if err == nil {
			t.Fatal("err = nil, want checksum mismatch error")
		}
	})

	t.Run("path traversal entry is rejected", func(t *testing.T) {
		body, sum := makeTarGz(t, map[string]string{"../escape": "evil"})
		srv := serve(t, body)
		dest := t.TempDir()

		err := downloadVerifyExtract(context.Background(), srv.Client(),
			PythonBuild{URL: srv.URL, SHA256: sum}, dest)
		if err == nil {
			t.Fatal("err = nil, want path-traversal rejection")
		}
	})
}

func TestEnsurePythonBranches(t *testing.T) {
	t.Run("system python3.11 on PATH is used", func(t *testing.T) {
		got, err := EnsurePython(context.Background(), EnsureOpts{
			Home:   t.TempDir(),
			GOOS:   "linux",
			GOARCH: "amd64",
			LookPath: func(name string) (string, error) {
				if name == "python3.11" {
					return "/usr/bin/python3.11", nil
				}
				return "", os.ErrNotExist
			},
			RunVersion: func(context.Context, string) (int, int, error) { return 3, 11, nil },
			Stat:       func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
			Client:     &http.Client{Transport: errRoundTripper{t}},
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != "/usr/bin/python3.11" {
			t.Errorf("path = %q, want the system interpreter", got)
		}
	})

	t.Run("managed python at the pinned version is reused when present", func(t *testing.T) {
		home := t.TempDir()
		managed := filepath.Join(home, "python", "bin", "python3.11")
		if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(managed, []byte("#!/fake"), 0o700); err != nil {
			t.Fatal(err)
		}
		// A COMPLETE managed install records the pinned version in its sentinel;
		// only then may EnsurePython short-circuit (symmetric with Postgres).
		if err := os.WriteFile(filepath.Join(home, "python", pyVersionFile), []byte(pyVersion), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := EnsurePython(context.Background(), EnsureOpts{
			Home:     home,
			GOOS:     "linux",
			GOARCH:   "amd64",
			LookPath: func(string) (string, error) { return "", os.ErrNotExist },
			Stat:     os.Stat,
			Client:   &http.Client{Transport: errRoundTripper{t}},
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != managed {
			t.Errorf("path = %q, want managed %q", got, managed)
		}
	})

	t.Run("managed pinned build wins over a host python3.11", func(t *testing.T) {
		// Regression for #742: the pre-fix EnsurePython did LookPath("python3.11")
		// BEFORE checking the managed tree, so a host interpreter outranked the
		// pinned, checksum-verified managed build. The managed build must win.
		home := t.TempDir()
		managed := filepath.Join(home, "python", "bin", "python3.11")
		if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(managed, []byte("#!/fake"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "python", pyVersionFile), []byte(pyVersion), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := EnsurePython(context.Background(), EnsureOpts{
			Home:     home,
			GOOS:     "linux",
			GOARCH:   "amd64",
			LookPath: func(string) (string, error) { return "/usr/bin/python3.11", nil },
			Stat:     os.Stat,
			Client:   &http.Client{Transport: errRoundTripper{t}},
		})
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if got != managed {
			t.Errorf("path = %q, want the managed build %q to outrank the host python3.11", got, managed)
		}
	})

	t.Run("unsupported platform errors before any download", func(t *testing.T) {
		_, err := EnsurePython(context.Background(), EnsureOpts{
			Home:     t.TempDir(),
			GOOS:     "windows",
			GOARCH:   "amd64",
			LookPath: func(string) (string, error) { return "", os.ErrNotExist },
			Stat:     func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		})
		if err == nil {
			t.Fatal("err = nil, want unsupported-platform error")
		}
	})
}

// TestEnsurePythonFailedDownloadKeepsExistingInstall locks the crash-safety
// invariant of #754: EnsurePython runs fetchVerify (download + checksum) BEFORE
// os.RemoveAll of the managed tree, so a download that errors — the network is
// down, the mirror 500s, the checksum drifts — must leave a working managed
// interpreter untouched on disk rather than destroying it and leaving nothing.
//
// The setup is the realistic upgrade case: a complete managed install of an
// OLDER version is present (its sentinel forces EnsurePython past the
// short-circuit and into the download), and the download fails. The pre-existing
// binary must survive.
func TestEnsurePythonFailedDownloadKeepsExistingInstall(t *testing.T) {
	home := t.TempDir()
	managed := filepath.Join(home, "python", "bin", "python3.11")
	if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("#!/existing interpreter"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Sentinel records an OLDER version, so EnsurePython reaches the download
	// instead of short-circuiting on the present-and-current install.
	if err := os.WriteFile(filepath.Join(home, "python", pyVersionFile), []byte("3.10.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := &recordingRoundTripper{}
	_, err := EnsurePython(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "amd64",
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Stat:     os.Stat,
		Client:   &http.Client{Transport: rt},
	})
	if err == nil {
		t.Fatal("err = nil, want the failed download surfaced as an error")
	}
	if !rt.called {
		t.Fatal("expected EnsurePython to attempt the download")
	}
	// The invariant: the pre-existing managed interpreter is still on disk,
	// byte-for-byte, because the failed download returned before os.RemoveAll.
	got, rerr := os.ReadFile(managed)
	if rerr != nil {
		t.Fatalf("existing managed interpreter was destroyed by a failed download: %v", rerr)
	}
	if string(got) != "#!/existing interpreter" {
		t.Errorf("managed interpreter = %q, want the pre-existing content intact", got)
	}
}

func TestExtractTarGzDirAndSymlink(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// directory entry
	if err := tw.WriteHeader(&tar.Header{Name: "python/lib", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	// regular file
	if err := tw.WriteHeader(&tar.Header{Name: "python/bin/python3.11", Typeflag: tar.TypeReg, Mode: 0o755, Size: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	// symlink entry (install_only archives include these)
	if err := tw.WriteHeader(&tar.Header{Name: "python/bin/python", Typeflag: tar.TypeSymlink, Linkname: "python3.11"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	sum := sha256.Sum256(buf.Bytes())
	srv := serve(t, buf.Bytes())
	if err := downloadVerifyExtract(context.Background(), srv.Client(),
		PythonBuild{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, dest); err != nil {
		t.Fatalf("err = %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dest, "python", "lib")); err != nil || !fi.IsDir() {
		t.Errorf("dir entry not extracted: %v", err)
	}
	link, err := os.Readlink(filepath.Join(dest, "python", "bin", "python"))
	if err != nil || link != "python3.11" {
		t.Errorf("symlink = %q err = %v, want -> python3.11", link, err)
	}
}

// TestEnsurePythonReextractsOnVersionMismatch proves the presence guard is
// version-aware (#742): when the managed CPython on disk records a DIFFERENT
// version than the pinned one, EnsurePython re-installs (reaches the download)
// instead of silently keeping the stale interpreter. Without a sentinel it would
// short-circuit forever, so a half-upgraded install would keep running an
// unsupported interpreter and fail later at an unrelated component.
func TestEnsurePythonReextractsOnVersionMismatch(t *testing.T) {
	home := t.TempDir()
	managed := filepath.Join(home, "python", "bin", "python3.11")
	if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("#!/fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Sentinel records an OLDER managed version than the pinned pyVersion.
	if err := os.WriteFile(filepath.Join(home, "python", pyVersionFile), []byte("3.10.0"), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := &recordingRoundTripper{}
	_, err := EnsurePython(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "amd64",
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Stat:     os.Stat,
		Client:   &http.Client{Transport: rt},
	})
	if err == nil {
		t.Fatal("err = nil, want a download attempt on version change")
	}
	if !rt.called {
		t.Error("expected a re-download when the on-disk CPython version differs from the pinned one")
	}
}

// TestEnsurePythonReextractsWhenVersionUnknown proves a managed install with no
// version sentinel (a pre-#742 install, or an interrupted extract) is treated as
// needing re-installation rather than trusted blindly.
func TestEnsurePythonReextractsWhenVersionUnknown(t *testing.T) {
	home := t.TempDir()
	managed := filepath.Join(home, "python", "bin", "python3.11")
	if err := os.MkdirAll(filepath.Dir(managed), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte("#!/fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	rt := &recordingRoundTripper{}
	_, err := EnsurePython(context.Background(), EnsureOpts{
		Home: home, GOOS: "linux", GOARCH: "amd64",
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Stat:     os.Stat,
		Client:   &http.Client{Transport: rt},
	})
	if err == nil {
		t.Fatal("err = nil, want a download attempt when no version is recorded")
	}
	if !rt.called {
		t.Error("expected a re-download when no version sentinel is present")
	}
}

// TestExtractEntryHandlesTypeChange proves a re-extract whose archive changed an
// entry's TYPE is idempotent (#729 follow-up): a TypeReg landing where a prior
// extract left a directory must not fail EISDIR, and a TypeSymlink landing where
// a non-empty directory exists must not fail with a non-IsNotExist os.Remove.
func TestExtractEntryHandlesTypeChange(t *testing.T) {
	t.Run("regular file over an existing directory", func(t *testing.T) {
		dest := t.TempDir()
		// First layout: python/foo is a directory (holds a child file).
		first, _ := makeTarGz(t, map[string]string{"python/foo/child": "x"})
		if err := extractTarGz(first, dest); err != nil {
			t.Fatalf("first extract: %v", err)
		}
		// Second layout: python/foo is now a regular file — a type change.
		second, _ := makeTarGz(t, map[string]string{"python/foo": "iam a file now"})
		if err := extractTarGz(second, dest); err != nil {
			t.Fatalf("re-extract over a directory must not fail EISDIR: %v", err)
		}
		got, rerr := os.ReadFile(filepath.Join(dest, "python", "foo"))
		if rerr != nil || string(got) != "iam a file now" {
			t.Errorf("python/foo = %q (err %v), want the new file content", got, rerr)
		}
	})

	t.Run("symlink over an existing non-empty directory", func(t *testing.T) {
		dest := t.TempDir()
		// Pre-existing non-empty directory where a symlink will later belong.
		linkDir := filepath.Join(dest, "python", "link")
		if err := os.MkdirAll(linkDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(linkDir, "leftover"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Archive turns python/link into a symlink to a sibling file.
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: "python/target", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("ok")); err != nil {
			t.Fatal(err)
		}
		if err := tw.WriteHeader(&tar.Header{Name: "python/link", Typeflag: tar.TypeSymlink, Linkname: "target"}); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := extractTarGz(buf.Bytes(), dest); err != nil {
			t.Fatalf("symlink over a non-empty directory must not fail: %v", err)
		}
		link, rerr := os.Readlink(filepath.Join(dest, "python", "link"))
		if rerr != nil || link != "target" {
			t.Errorf("symlink = %q err = %v, want -> target", link, rerr)
		}
	})
}

func TestDownloadVerifyExtractErrors(t *testing.T) {
	t.Run("non-200 status is an error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		err := downloadVerifyExtract(context.Background(), srv.Client(),
			PythonBuild{URL: srv.URL, SHA256: "x"}, t.TempDir())
		if err == nil {
			t.Fatal("err = nil, want non-200 error")
		}
	})

	t.Run("non-gzip body with matching checksum fails to extract", func(t *testing.T) {
		body := []byte("this is not a gzip stream")
		sum := sha256.Sum256(body)
		srv := serve(t, body)
		err := downloadVerifyExtract(context.Background(), srv.Client(),
			PythonBuild{URL: srv.URL, SHA256: hex.EncodeToString(sum[:])}, t.TempDir())
		if err == nil {
			t.Fatal("err = nil, want gzip-open error")
		}
	})
}
