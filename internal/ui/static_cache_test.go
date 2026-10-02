package ui

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

// countingFS counts how often each file is opened, so a test can tell a
// per-request read from a cached one.
type countingFS struct {
	fs.FS
	mu    sync.Mutex
	opens map[string]int
}

func (c *countingFS) Open(name string) (fs.File, error) {
	c.mu.Lock()
	c.opens[name]++
	c.mu.Unlock()
	return c.FS.Open(name)
}

func staticGet(t *testing.T, h http.Handler, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestStaticAnswersConditionalRequestWith304 pins that every static file carries
// a strong ETag and that a revalidation with that ETag costs no body.
func TestStaticAnswersConditionalRequestWith304(t *testing.T) {
	h := fixture().StaticHandler()
	for _, enc := range []string{"gzip", ""} {
		first := staticGet(t, h, "/assets/app-abc123.js", map[string]string{"Accept-Encoding": enc})
		etag := first.Header().Get("ETag")
		if etag == "" || strings.HasPrefix(etag, "W/") {
			t.Fatalf("encoding %q: ETag = %q, want a strong ETag", enc, etag)
		}
		again := staticGet(t, h, "/assets/app-abc123.js", map[string]string{
			"Accept-Encoding": enc, "If-None-Match": etag,
		})
		if again.Code != http.StatusNotModified {
			t.Fatalf("encoding %q: conditional GET = %d, want 304", enc, again.Code)
		}
		if again.Body.Len() != 0 {
			t.Errorf("encoding %q: 304 carried a %d-byte body", enc, again.Body.Len())
		}
		if cc := again.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
			t.Errorf("encoding %q: 304 Cache-Control = %q, want the immutable policy", enc, cc)
		}
	}
}

// TestStaticETagDiffersPerEncoding pins that the gzip and identity bodies, which
// are different bytes, never share a strong validator.
func TestStaticETagDiffersPerEncoding(t *testing.T) {
	h := fixture().StaticHandler()
	gz := staticGet(t, h, "/assets/app-abc123.js", map[string]string{"Accept-Encoding": "gzip"})
	id := staticGet(t, h, "/assets/app-abc123.js", nil)
	if gz.Header().Get("ETag") == id.Header().Get("ETag") {
		t.Errorf("gzip and identity share ETag %q", gz.Header().Get("ETag"))
	}
	if !strings.Contains(id.Header().Get("Vary"), "Accept-Encoding") {
		t.Errorf("identity response of a compressible file lacks Vary: Accept-Encoding")
	}
}

// TestStaticReadsAndCompressesEachFileOnce pins that the bundle is read and
// gzipped once per process, not on every request.
func TestStaticReadsAndCompressesEachFileOnce(t *testing.T) {
	data := []byte(strings.Repeat("console.log('hi');", 100))
	cfs := &countingFS{FS: fstest.MapFS{"assets/app.js": {Data: data}}, opens: map[string]int{}}
	h := NewFromFS(cfs, "test").StaticHandler()
	for range 3 {
		if rec := staticGet(t, h, "/assets/app.js", map[string]string{"Accept-Encoding": "gzip"}); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if n := cfs.opens["assets/app.js"]; n != 1 {
		t.Errorf("file opened %d times across 3 gzip requests, want 1", n)
	}
}

// TestStaticServesIdentityFromTheBundle pins that the cache keeps no second
// copy of the raw bundle: an identity request streams the file from the asset
// filesystem (embedded in the binary in production), with Range support.
func TestStaticServesIdentityFromTheBundle(t *testing.T) {
	data := []byte(strings.Repeat("console.log('hi');", 100))
	cfs := &countingFS{FS: fstest.MapFS{"assets/app.js": {Data: data}}, opens: map[string]int{}}
	srv := NewFromFS(cfs, "test")
	srv.Precompress()
	h := srv.StaticHandler()
	for range 3 {
		rec := staticGet(t, h, "/assets/app.js", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != string(data) {
			t.Fatalf("identity: status %d, %d bytes, want 200 and the file", rec.Code, rec.Body.Len())
		}
	}
	if n := cfs.opens["assets/app.js"]; n != 4 {
		t.Errorf("file opened %d times (1 build + 3 identity requests), want 4", n)
	}
	rec := staticGet(t, h, "/assets/app.js", map[string]string{"Range": "bytes=0-9"})
	if rec.Code != http.StatusPartialContent || rec.Body.String() != string(data[:10]) {
		t.Errorf("range: status %d, body %q, want 206 and the first 10 bytes", rec.Code, rec.Body.String())
	}
}

// TestPrecompressBuildsCompressibleFilesUpFront pins the startup warm-up: after
// Precompress a request for a compressible file reads nothing, and binaries that
// are never gzipped are left for their first request.
func TestPrecompressBuildsCompressibleFilesUpFront(t *testing.T) {
	cfs := &countingFS{FS: fstest.MapFS{
		"assets/app.js": {Data: []byte("console.log('hi')")},
		"pin_32.png":    {Data: []byte("\x89PNG")},
	}, opens: map[string]int{}}
	srv := NewFromFS(cfs, "test")
	srv.Precompress()
	if n := cfs.opens["assets/app.js"]; n != 1 {
		t.Fatalf("Precompress opened app.js %d times, want 1", n)
	}
	if n := cfs.opens["pin_32.png"]; n != 0 {
		t.Errorf("Precompress opened a png %d times, want 0", n)
	}
	rec := staticGet(t, srv.StaticHandler(), "/assets/app.js", map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status %d, encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	if n := cfs.opens["assets/app.js"]; n != 1 {
		t.Errorf("request after Precompress reopened app.js (%d opens)", n)
	}
}

// TestStaticMissingFileIsNotCached pins that 404 paths do not grow the cache.
func TestStaticMissingFileIsNotCached(t *testing.T) {
	srv := fixture()
	h := srv.StaticHandler()
	for range 2 {
		if rec := staticGet(t, h, "/assets/nope.js", nil); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	}
	if _, ok := srv.static.entries.Load("assets/nope.js"); ok {
		t.Error("a missing file left an entry in the static cache")
	}
}
