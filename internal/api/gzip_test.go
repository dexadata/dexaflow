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
