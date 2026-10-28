package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// bodyLimitRouter serves one JSON route and one upload route behind BodyLimit.
// Both decode the body the way the real handlers do and answer 400 when it does
// not decode.
func bodyLimitRouter(requestMax, uploadMax int64, ran *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(BodyLimit(requestMax, uploadMax))
	h := func(c *gin.Context) {
		*ran = true
		var body map[string]any
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"detail": "invalid body"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"keys": len(body)})
	}
	r.POST("/api/v2/variables", h)
	r.POST("/api/v2/dags/:dag_id/versions", h)
	return r
}

// jsonBody returns a JSON object of about n bytes.
func jsonBody(n int) string {
	return `{"v":"` + strings.Repeat("x", n) + `"}`
}

func serveBody(t *testing.T, r http.Handler, path, body string, chunked bool) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader = strings.NewReader(body)
	if chunked {
		// No Len method, so the request goes out without a Content-Length.
		rd = io.MultiReader(rd)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, rd)
	if chunked {
		req.ContentLength = -1
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func assertTooLarge(t *testing.T, w *httptest.ResponseRecorder, limit int) {
	t.Helper()
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var p Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not one problem: %v (%s)", err, w.Body.String())
	}
	if p.Status != http.StatusRequestEntityTooLarge || !strings.Contains(p.Detail, "("+strconv.Itoa(limit)+" bytes)") {
		t.Errorf("problem = %+v, want status 413 naming the %d byte limit", p, limit)
	}
}

// TestBodyLimitRefusesDeclaredLengthBeforeTheHandler pins the cheap path: a
// Content-Length over the limit is answered 413 without running the handler.
func TestBodyLimitRefusesDeclaredLengthBeforeTheHandler(t *testing.T) {
	var ran bool
	w := serveBody(t, bodyLimitRouter(1024, 0, &ran), "/api/v2/variables", jsonBody(2048), false)
	assertTooLarge(t, w, 1024)
	if ran {
		t.Error("the handler ran for a body refused by its Content-Length")
	}
}

// TestBodyLimitCutsAChunkedBodyAndAnswers413 pins the streamed path: a body
// without a Content-Length is cut at the limit, and the 400 the handler answers
// to the truncated JSON becomes the same 413 problem.
func TestBodyLimitCutsAChunkedBodyAndAnswers413(t *testing.T) {
	var ran bool
	w := serveBody(t, bodyLimitRouter(1024, 0, &ran), "/api/v2/variables", jsonBody(4096), true)
	assertTooLarge(t, w, 1024)
	if strings.Contains(w.Body.String(), "invalid body") {
		t.Errorf("the handler's own body leaked into the 413: %s", w.Body.String())
	}
}

// TestBodyLimitPassesBodiesUnderTheLimit pins that the limit is the only change:
// a body under it, declared or chunked, reaches the handler whole.
func TestBodyLimitPassesBodiesUnderTheLimit(t *testing.T) {
	for _, chunked := range []bool{false, true} {
		var ran bool
		w := serveBody(t, bodyLimitRouter(1024, 0, &ran), "/api/v2/variables", jsonBody(512), chunked)
		if w.Code != http.StatusOK {
			t.Errorf("chunked=%v: status = %d, want 200; body %s", chunked, w.Code, w.Body.String())
		}
	}
}

// TestBodyLimitKeepsHandlerErrorsUnderTheLimit pins that a 400 for a body that
// fit is the handler's own answer, not a 413.
func TestBodyLimitKeepsHandlerErrorsUnderTheLimit(t *testing.T) {
	var ran bool
	w := serveBody(t, bodyLimitRouter(1024, 0, &ran), "/api/v2/variables", "{not json", true)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid body") {
		t.Errorf("status = %d body %s, want the handler's 400", w.Code, w.Body.String())
	}
}

// TestBodyLimitGivesUploadRoutesTheirOwnLimit pins the split: the upload route
// is held to server.max_upload_body_bytes, and stays unlimited while that is 0
// even with a request limit set, so a JSON-sized cap cannot refuse a deploy.
func TestBodyLimitGivesUploadRoutesTheirOwnLimit(t *testing.T) {
	const upload = "/api/v2/dags/d/versions"
	var ran bool
	if w := serveBody(t, bodyLimitRouter(1024, 0, &ran), upload, jsonBody(64<<10), false); w.Code != http.StatusOK {
		t.Errorf("upload limit 0: status = %d, want 200", w.Code)
	}
	if w := serveBody(t, bodyLimitRouter(1024, 8192, &ran), upload, jsonBody(4096), false); w.Code != http.StatusOK {
		t.Errorf("4 KiB under an 8 KiB upload limit: status = %d, want 200", w.Code)
	}
	assertTooLarge(t, serveBody(t, bodyLimitRouter(1024, 8192, &ran), upload, jsonBody(16<<10), true), 8192)
	// The upload limit does not widen the other routes.
	assertTooLarge(t, serveBody(t, bodyLimitRouter(1024, 8192, &ran), "/api/v2/variables", jsonBody(4096), false), 1024)
}

// TestBodyLimitZeroLeavesBodiesUnlimited pins the ADR 0062 gate: with both
// limits at 0 a large body reaches the handler as before.
func TestBodyLimitZeroLeavesBodiesUnlimited(t *testing.T) {
	var ran bool
	w := serveBody(t, bodyLimitRouter(0, 0, &ran), "/api/v2/variables", jsonBody(1<<20), true)
	if w.Code != http.StatusOK {
		t.Errorf("limits 0: status = %d, want 200", w.Code)
	}
}

// TestUploadRoutesAreRegistered pins uploadRoutes to the real router, so a
// renamed deploy route cannot silently fall back to the smaller request limit.
func TestUploadRoutesAreRegistered(t *testing.T) {
	srv := newFullScopeServer(t)
	registered := map[string]bool{}
	for _, ri := range srv.Routes() {
		registered[ri.Method+" "+ri.Path] = true
	}
	for key := range uploadRoutes {
		if !registered[key] {
			t.Errorf("uploadRoutes names %q, which the router does not register", key)
		}
	}
}
