package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/logs"
)

// slowTailReader emits n live lines, one every gap, then ends the tail.
type slowTailReader struct {
	n   int
	gap time.Duration
}

func (s *slowTailReader) ReadLogs(context.Context, string, string, string, string, int) (io.ReadCloser, error) {
	return nil, ErrNotFound
}

func (s *slowTailReader) Tail(ctx context.Context, _, _, _, _ string, _ int) (lines <-chan string, cancel func(), err error) {
	ch := make(chan string)
	go func() {
		defer close(ch)
		for range s.n {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.gap):
			}
			select {
			case <-ctx.Done():
				return
			case ch <- logs.EncodeLine(logs.Event{Level: "info", Stream: "stdout", Message: "tick", Time: time.Now().UTC()}):
			}
		}
	}()
	return ch, func() {}, nil
}

// TestLogTailOutlivesTheServerReadTimeout pins that a configured
// server.read_timeout bounds reading a request, never a live tail. net/http
// clears the read deadline once the request body is consumed, and no write
// deadline is set, so a tail keeps streaming past the read timeout. The test
// guards that standard library behavior.
func TestLogTailOutlivesTheServerReadTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	reader := &slowTailReader{n: 5, gap: 100 * time.Millisecond}
	r.GET("/plain", func(c *gin.Context) { tailLogs(c, reader, 1, storedTail{}) })
	r.GET("/ndjson", func(c *gin.Context) { tailNdjson(c, reader, 1, storedTail{}) })

	srv := httptest.NewUnstartedServer(r)
	srv.Config.ReadTimeout = 150 * time.Millisecond
	srv.Start()
	defer srv.Close()

	for _, path := range []string{"/plain", "/ndjson"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		lines := 0
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			lines++
		}
		_ = resp.Body.Close()
		if lines != 5 {
			t.Errorf("%s: tail delivered %d of 5 lines across a 150ms read timeout", path, lines)
		}
	}
}
