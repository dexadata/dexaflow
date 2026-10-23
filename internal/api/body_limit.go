package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// uploadRoutes are the routes that take a DAG's code or spec, which can be far
// larger than any other request: they get server.max_upload_body_bytes instead
// of server.max_request_body_bytes. Keyed by method and gin route pattern.
var uploadRoutes = map[string]bool{
	http.MethodPost + " /api/v2/dags/:dag_id/versions":         true,
	http.MethodPost + " /api/v2/dags/:dag_id/versions/dry_run": true,
	http.MethodPut + " /api/v2/ide/file":                       true,
	http.MethodPost + " /api/v2/ide/file":                      true,
}

// BodyLimit caps the size of request bodies (#1613): requestMax for every
// route, uploadMax for the routes in uploadRoutes. A limit of 0 leaves those
// routes unlimited, so with both at 0 (the default) nothing changes and the
// middleware is not wired.
//
// A body whose Content-Length is over the limit is refused with 413 before the
// handler runs. A body without one (chunked) is cut at the limit while the
// handler reads it; the handler then fails to decode it and answers a 4xx,
// which is replaced by the same 413 problem, so the client sees one answer
// whichever way the body arrived.
func BodyLimit(requestMax, uploadMax int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := requestMax
		if uploadRoutes[c.Request.Method+" "+c.FullPath()] {
			limit = uploadMax
		}
		if limit <= 0 || c.Request.Body == nil || c.Request.Body == http.NoBody {
			c.Next()
			return
		}
		if c.Request.ContentLength > limit {
			AbortProblem(c, http.StatusRequestEntityTooLarge, "Request body too large", bodyTooLargeDetail(limit))
			return
		}
		body := &limitedBody{ReadCloser: http.MaxBytesReader(c.Writer, c.Request.Body, limit)}
		c.Request.Body = body
		w := &bodyLimitWriter{ResponseWriter: c.Writer, body: body, limit: limit, path: c.Request.URL.Path}
		c.Writer = w
		defer func() { c.Writer = w.ResponseWriter }()
		c.Next()
	}
}

func bodyTooLargeDetail(limit int64) string {
	return fmt.Sprintf("the request body is larger than this route accepts (%d bytes)", limit)
}

// limitedBody records whether the handler read past the limit.
type limitedBody struct {
	io.ReadCloser
	exceeded bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		b.exceeded = true
	}
	return n, err
}

// bodyLimitWriter turns the 4xx a handler answers to a body cut at the limit
// into a 413 problem, dropping the handler's own body, which only says the
// (truncated) body did not decode.
type bodyLimitWriter struct {
	gin.ResponseWriter
	body     *limitedBody
	limit    int64
	path     string
	replaced bool
	written  bool
}

// WriteHeader answers 413 in place of a 4xx once the body was cut.
func (w *bodyLimitWriter) WriteHeader(code int) {
	if w.body.exceeded && code >= 400 && code < 500 {
		w.replaced = true
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Del("Content-Length")
		code = http.StatusRequestEntityTooLarge
	}
	w.ResponseWriter.WriteHeader(code)
}

// Write sends the 413 problem once in place of the handler's body.
func (w *bodyLimitWriter) Write(p []byte) (int, error) {
	if !w.replaced {
		return w.ResponseWriter.Write(p)
	}
	if !w.written {
		w.written = true
		out, err := json.Marshal(Problem{
			Type:     "about:blank",
			Title:    "Request body too large",
			Status:   http.StatusRequestEntityTooLarge,
			Detail:   bodyTooLargeDetail(w.limit),
			Instance: w.path,
		})
		if err != nil {
			return 0, err
		}
		if _, err := w.ResponseWriter.Write(out); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// WriteString is Write for gin's string renderers.
func (w *bodyLimitWriter) WriteString(s string) (int, error) {
	if !w.replaced {
		return w.ResponseWriter.WriteString(s)
	}
	return w.Write([]byte(s)) //nolint:gocritic // the bytes are dropped for the problem
}
