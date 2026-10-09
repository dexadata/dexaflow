package api

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

var bigJSON = gin.H{"items": strings.Repeat("dag_id task_id state ", 200)} // about 4 KB

func gzipEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GzipJSON())
	r.GET("/api/v2/big", func(c *gin.Context) { c.JSON(http.StatusOK, bigJSON) })
	r.GET("/api/v2/small", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.GET("/ui/stream", func(c *gin.Context) {
		c.Header("Content-Type", "application/x-ndjson")
		for range 100 {
			_, _ = c.Writer.WriteString(`{"line":"` + strings.Repeat("x", 40) + `"}` + "\n")
		}
	})
	r.GET("/api/v2/text", func(c *gin.Context) { c.String(http.StatusOK, strings.Repeat("plain ", 500)) })
	r.GET("/api/v2/dags/d/dagRuns/r/taskInstances/t/logs/1", func(c *gin.Context) {
		c.Header("Content-Type", "application/x-ndjson")
		_, _ = c.Writer.WriteString(strings.Repeat(`{"event":"x"}`+"\n", 200))
	})
	r.GET("/api/v2/tail", func(c *gin.Context) {
		c.Header("Content-Type", "application/x-ndjson")
		_, _ = c.Writer.WriteString(`{"line":"first"}` + "\n")
		c.Writer.Flush()
		_, _ = c.Writer.WriteString(strings.Repeat(`{"line":"more"}`+"\n", 200))
	})
	r.GET("/ui/notmodified", func(c *gin.Context) {
		c.Header("Content-Type", "application/x-ndjson")
		c.Status(http.StatusNotModified)
	})
	return r
}

func gzipGet(t *testing.T, r http.Handler, path string, acceptGzip bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, path, http.NoBody)
	if acceptGzip {
		req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func gunzip(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestGzipJSONCompressesLargeJSONAndNDJSON pins the point of the middleware:
// JSON and NDJSON bodies over 1 KB go out gzipped when the client accepts it.
func TestGzipJSONCompressesLargeJSONAndNDJSON(t *testing.T) {
	r := gzipEngine()
	plain := gzipGet(t, r, "/api/v2/big", false).Body.String()
	for _, path := range []string{"/api/v2/big", "/ui/stream"} {
		rec := gzipGet(t, r, path, true)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatalf("%s: status %d, encoding %q, want gzip", path, rec.Code, rec.Header().Get("Content-Encoding"))
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Encoding") {
			t.Errorf("%s: missing Vary: Accept-Encoding", path)
		}
		body := gunzip(t, rec)
		if path == "/api/v2/big" && body != plain {
			t.Errorf("decompressed body differs from the identity body")
		}
	}
}

// TestGzipJSONLeavesOtherResponsesAlone pins every case that must stay
// identity: small bodies, clients without gzip, non-JSON, log routes, anything
// that flushes (a stream), and bodiless responses.
func TestGzipJSONLeavesOtherResponsesAlone(t *testing.T) {
	r := gzipEngine()
	cases := []struct {
		path   string
		accept bool
		status int
	}{
		{"/api/v2/small", true, http.StatusOK},
		{"/api/v2/big", false, http.StatusOK},
		{"/api/v2/text", true, http.StatusOK},
		{"/api/v2/dags/d/dagRuns/r/taskInstances/t/logs/1", true, http.StatusOK},
		{"/api/v2/tail", true, http.StatusOK},
		{"/ui/notmodified", true, http.StatusNotModified},
	}
	for _, tc := range cases {
		rec := gzipGet(t, r, tc.path, tc.accept)
		if rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d", tc.path, rec.Code, tc.status)
		}
		if enc := rec.Header().Get("Content-Encoding"); enc != "" {
			t.Errorf("%s: Content-Encoding %q, want identity", tc.path, enc)
		}
	}
	if body := gzipGet(t, r, "/api/v2/tail", true).Body.String(); !strings.HasPrefix(body, `{"line":"first"}`) {
		t.Errorf("streamed body lost its first line: %q", body[:min(len(body), 40)])
	}
}

// TestGzipJSONIsOffByDefault pins the gate (server.gzip_responses): without
// the option the API server sends identity bodies as before.
func TestGzipJSONIsOffByDefault(t *testing.T) {
	srv := NewServer(Dependencies{Logger: discardLogger(), CORSOrigins: []string{"*"}})
	rec := gzipGet(t, srv, "/api/v2/version", true)
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q with the gate off, want identity", enc)
	}
}

// TestGzipJSONLetsRecoveryAnswerAPanic pins that a handler panic still ends
// in gin.Recovery's 500: the middleware must not flush a 200 with an empty
// body on its way out.
func TestGzipJSONLetsRecoveryAnswerAPanic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(gin.RecoveryWithWriter(io.Discard))
	r.Use(GzipJSON())
	r.GET("/api/v2/boom", func(c *gin.Context) {
		c.Header("Content-Type", "application/json")
		panic("boom")
	})
	rec := gzipGet(t, r, "/api/v2/boom", true)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d after a handler panic, want 500", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding %q on the panic response, want identity", enc)
	}
}

// TestGzipJSONSkipsSecretBearingRoutes pins the BREACH mitigation: routes that
// return secrets or tokens (variables, connections, XComs, auth) are never
// compressed, so their length cannot leak a secret next to reflected input.
func TestGzipJSONSkipsSecretBearingRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GzipJSON())
	paths := []string{
		"/api/v2/variables",
		"/api/v2/variables/db_password",
		"/api/v2/connections",
		"/api/v2/connections/pg",
		"/api/v2/xcoms/etl/r1/extract/key",
		"/api/v2/dags/etl/dagRuns/r1/taskInstances/extract/xcomEntries",
		"/api/v2/auth/token/renew",
		"/ui/auth/token",
		"/ui/auth/me",
		"/api/v2/ide/file",
		"/api/v2/dagSources/etl",
		"/api/v2/dags/etl/dagRuns/r1/taskInstances/extract",
	}
	for _, p := range paths {
		r.GET(p, func(c *gin.Context) { c.JSON(http.StatusOK, bigJSON) })
	}
	for _, p := range paths {
		rec := gzipGet(t, r, p, true)
		if enc := rec.Header().Get("Content-Encoding"); enc != "" {
			t.Errorf("%s: Content-Encoding %q, want identity", p, enc)
		}
	}
}

// TestGzipJSONHonorsAcceptEncodingQuality pins the Accept-Encoding parse: a
// coding with q=0 is refused, not accepted because its name appears.
func TestGzipJSONHonorsAcceptEncodingQuality(t *testing.T) {
	r := gzipEngine()
	cases := map[string]bool{
		"gzip":                 true,
		"br, GZIP;q=0.5":       true,
		"gzip;q=0":             false,
		"gzip; q=0.000":        false,
		"identity, x-gzip;q=0": false,
		"deflate":              false,
		"nogzip":               false,
	}
	for ae, want := range cases {
		req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/big", http.NoBody)
		req.Header.Set("Accept-Encoding", ae)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if got := rec.Header().Get("Content-Encoding") == "gzip"; got != want {
			t.Errorf("Accept-Encoding %q: gzipped=%v, want %v", ae, got, want)
		}
	}
}

// TestGzipJSONNeverCompressesPartialContent pins that only full bodies are
// compressed: a 206 keeps an identity Content-Range, so gzipping it would
// break the range.
func TestGzipJSONNeverCompressesPartialContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GzipJSON())
	r.GET("/api/v2/partial", func(c *gin.Context) {
		c.Header("Content-Range", "bytes 0-1999/5000")
		c.JSON(http.StatusPartialContent, bigJSON)
	})
	rec := gzipGet(t, r, "/api/v2/partial", true)
	if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Encoding") != "" {
		t.Errorf("206: status %d Content-Encoding %q, want 206 identity", rec.Code, rec.Header().Get("Content-Encoding"))
	}
}

// TestGzipJSONVariesOnEveryEligibleResponse pins that a cache sees
// Vary: Accept-Encoding on identity answers of an eligible route too, so it
// never serves a stored identity body to a gzip client or the reverse.
func TestGzipJSONVariesOnEveryEligibleResponse(t *testing.T) {
	r := gzipEngine()
	for _, path := range []string{"/api/v2/small", "/api/v2/big", "/ui/notmodified"} {
		rec := gzipGet(t, r, path, true)
		if got := rec.Header().Values("Vary"); len(got) != 1 || got[0] != "Accept-Encoding" {
			t.Errorf("%s: Vary %q, want exactly [Accept-Encoding]", path, got)
		}
	}
}

// TestGzipJSONKeepsAStatusSetBeforeTheChain pins that the wrapper starts from
// the writer's current status: a 404 set before the handlers run (gin's
// no-route path) is not turned into an empty 200.
func TestGzipJSONKeepsAStatusSetBeforeTheChain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Status(http.StatusNotFound) })
	r.Use(GzipJSON())
	r.GET("/api/v2/nothing", func(c *gin.Context) {})
	if rec := gzipGet(t, r, "/api/v2/nothing", true); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}
