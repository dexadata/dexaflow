package ui

import (
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// syntheticBundle returns about size bytes of JavaScript-like text with a fixed
// seed, so it compresses roughly like the real SPA bundle and is identical on
// every run.
func syntheticBundle(size int) []byte {
	words := []string{"function", "return", "const", "let", "this", "props", "state",
		"useEffect", "useState", "=>", "{", "}", "(", ")", ";", "null", "undefined",
		"dag_id", "task_id", "map_index", "try_number", "className", "children"}
	r := rand.New(rand.NewPCG(1, 2))
	var sb strings.Builder
	sb.Grow(size)
	for sb.Len() < size {
		sb.WriteString(words[r.IntN(len(words))])
		if r.IntN(7) == 0 {
			sb.WriteString(strings.Repeat("x", r.IntN(12)))
		}
		sb.WriteByte(' ')
	}
	return []byte(sb.String())
}

// BenchmarkStaticHandlerBundle measures serving one large JavaScript asset, the
// cost every cache-cold browser pays on first load.
func BenchmarkStaticHandlerBundle(b *testing.B) {
	fsys := fstest.MapFS{"assets/index.js": {Data: syntheticBundle(5 << 20)}}
	h := NewFromFS(fsys, "bench").StaticHandler()
	for _, enc := range []string{"gzip", "identity"} {
		b.Run("encoding="+enc, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				req := httptest.NewRequestWithContext(b.Context(), http.MethodGet, "/assets/index.js", http.NoBody)
				req.Header.Set("Accept-Encoding", enc)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					b.Fatalf("status %d", rec.Code)
				}
			}
		})
	}
}
