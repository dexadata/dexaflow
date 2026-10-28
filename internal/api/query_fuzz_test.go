package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// FuzzDecodeCursor feeds arbitrary `cursor` query values to the keyset
// pagination decoder. A client can send anything there; decodeCursor must
// never panic, and a cursor it accepts must encode back to one that decodes to
// the same position.
func FuzzDecodeCursor(f *testing.F) {
	f.Add(encodeCursor(domain.PageCursor{At: time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC), Key: "manual__2026-10-12"}))
	f.Add(encodeCursor(domain.PageCursor{At: time.Unix(0, 1).UTC(), Key: "42"}))
	f.Add(encodeCursor(domain.PageCursor{At: time.Unix(0, 1).UTC(), Key: "42", Total: 400000}))
	f.Add("")
	f.Add("not base64!")
	f.Add("eyJ0IjoiMjAyNi0xMC0xMlQwODowMDowMCswMjowMCIsImsiOiJ4In0") // a +02:00 offset
	f.Fuzz(func(t *testing.T, raw string) {
		cur, err := decodeCursor(raw)
		if err != nil {
			if !errors.Is(err, errBadCursor) {
				t.Fatalf("decodeCursor error = %v, want errBadCursor", err)
			}
			return
		}
		if cur.Key == "" {
			t.Fatalf("accepted cursor %+v from %q", cur, raw)
		}
		again, err := decodeCursor(encodeCursor(cur))
		if err != nil {
			t.Fatalf("re-encoded cursor rejected: %v", err)
		}
		if !again.At.Equal(cur.At) || again.Key != cur.Key || again.Total != cur.Total {
			t.Fatalf("round trip moved the cursor: got %+v, want %+v", again, cur)
		}
	})
}

// FuzzPagination feeds arbitrary query strings to the limit/offset parsing
// every list endpoint shares, with and without server.max_page_limit. The page
// size must stay positive and within the cap, and the offset non-negative,
// whatever the client sends.
func FuzzPagination(f *testing.F) {
	f.Add("limit=10&offset=20", 0)
	f.Add("limit=-1&offset=-5", 0)
	f.Add("limit=99999999999999999999&offset=1e3", 100)
	f.Add("limit=500&limit=1", 100)
	f.Add("offset=%zz", 0)
	gin.SetMode(gin.TestMode)
	f.Fuzz(func(t *testing.T, query string, maxLimit int) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/dags", http.NoBody)
		c.Request.URL.RawQuery = query
		if maxLimit != 0 {
			c.Set(contextKeyMaxPageLimit, maxLimit)
		}
		limit, offset := pagination(c)
		if limit <= 0 {
			t.Fatalf("pagination(%q) limit = %d, want > 0", query, limit)
		}
		if maxLimit > 0 && limit > maxLimit {
			t.Fatalf("pagination(%q) limit = %d above the cap %d", query, limit, maxLimit)
		}
		if offset < 0 {
			t.Fatalf("pagination(%q) offset = %d, want >= 0", query, offset)
		}
		if n := clampLimit(c.Query("limit"), 50, 500); n <= 0 || n > 500 {
			t.Fatalf("clampLimit(%q) = %d, want 1..500", c.Query("limit"), n)
		}
	})
}
