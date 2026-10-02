package api

import (
	"compress/gzip"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
)

// gzipMinBytes is the smallest body worth compressing: below it the gzip
// header and the CPU cost outweigh the bytes saved.
const gzipMinBytes = 1024

// gzipWriters reuses compressors across responses. BestSpeed: a dynamic body
// is compressed once per request, so CPU matters more than the last few bytes.
var gzipWriters = sync.Pool{New: func() any { return newGzipWriter() }}

func newGzipWriter() *gzip.Writer {
	gz, err := gzip.NewWriterLevel(nil, gzip.BestSpeed)
	if err != nil { // unreachable: BestSpeed is a valid level.
		return gzip.NewWriter(nil)
	}
	return gz
}

// GzipJSON compresses JSON and NDJSON responses on the API and UI surfaces
// (/api/v2/*, /ui/*) when the client accepts gzip and the body reaches 1 KB.
// It is wired only when server.gzip_responses is on.
//
// Streams are never compressed: log routes are skipped by path, and a handler
// that flushes before 1 KB is buffered (a live tail, an SSE feed) is served as
// identity from then on, so every line still reaches the client the moment it
// is flushed. Static assets are precompressed by their own handler and are
// not on these prefixes.
func GzipJSON() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if !strings.HasPrefix(p, "/api/v2/") && !strings.HasPrefix(p, "/ui/") ||
			strings.Contains(p, "/logs") ||
			!strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
			c.Next()
			return
		}
		w := &gzipResponseWriter{ResponseWriter: c.Writer, status: http.StatusOK}
		c.Writer = w
		defer func() {
			w.finish()
			c.Writer = w.ResponseWriter
		}()
		c.Next()
	}
}

// gzipResponseWriter holds back the first gzipMinBytes of the body to decide
// between gzip and identity, then passes everything through the chosen path.
type gzipResponseWriter struct {
	gin.ResponseWriter
	status  int
	buf     []byte
	decided bool
	gz      *gzip.Writer
}

// WriteHeader records the status until the encoding is decided.
func (w *gzipResponseWriter) WriteHeader(code int) {
	if !w.decided {
		w.status = code
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

// WriteHeaderNow is deferred until the encoding is decided.
func (w *gzipResponseWriter) WriteHeaderNow() {}

// Status reports the recorded status, sent or not.
func (w *gzipResponseWriter) Status() int {
	if !w.decided {
		return w.status
	}
	return w.ResponseWriter.Status()
}

// Written reports whether the headers have gone out.
func (w *gzipResponseWriter) Written() bool { return w.decided && w.ResponseWriter.Written() }

// WriteString is Write for a string.
func (w *gzipResponseWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s)) //nolint:gocritic // this IS the WriteString; it must route through Write.
}

// Write holds the body back until it reaches gzipMinBytes, then passes it on.
func (w *gzipResponseWriter) Write(p []byte) (int, error) {
	if w.decided {
		if w.gz != nil {
			return w.gz.Write(p)
		}
		return w.ResponseWriter.Write(p)
	}
	w.buf = append(w.buf, p...)
	if len(w.buf) >= gzipMinBytes {
		if err := w.decide(compressibleJSON(w.Header().Get("Content-Type"))); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush marks the response as a stream: an undecided body goes out as
// identity, and a compressed one flushes its pending gzip block.
func (w *gzipResponseWriter) Flush() {
	if !w.decided {
		if err := w.decide(false); err != nil {
			return
		}
	}
	if w.gz != nil {
		if err := w.gz.Flush(); err != nil {
			return
		}
	}
	w.ResponseWriter.Flush()
}

// decide sends the status line and headers, then the held-back bytes, either
// gzipped or as is.
func (w *gzipResponseWriter) decide(compress bool) error {
	w.decided = true
	h := w.Header()
	compress = compress && h.Get("Content-Encoding") == "" &&
		w.status >= http.StatusOK && w.status < 300 && w.status != http.StatusNoContent
	if compress {
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		h.Del("Content-Length")
		gz, ok := gzipWriters.Get().(*gzip.Writer)
		if !ok {
			gz = newGzipWriter()
		}
		gz.Reset(w.ResponseWriter)
		w.gz = gz
	}
	w.ResponseWriter.WriteHeader(w.status)
	buf := w.buf
	w.buf = nil
	if len(buf) == 0 {
		w.ResponseWriter.WriteHeaderNow()
		return nil
	}
	var err error
	if w.gz != nil {
		_, err = w.gz.Write(buf)
	} else {
		_, err = w.ResponseWriter.Write(buf)
	}
	return err
}

// finish writes whatever is still held back (a body under gzipMinBytes, or
// none) as identity and closes the gzip stream.
func (w *gzipResponseWriter) finish() {
	if !w.decided {
		if err := w.decide(false); err != nil {
			return
		}
	}
	if w.gz != nil {
		if err := w.gz.Close(); err != nil {
			return // client gone; nothing to recover.
		}
		gzipWriters.Put(w.gz)
		w.gz = nil
	}
}

// compressibleJSON reports whether a Content-Type is JSON or NDJSON.
func compressibleJSON(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "application/x-ndjson") ||
		strings.HasPrefix(ct, "application/problem+json")
}
