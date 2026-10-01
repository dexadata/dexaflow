package storage

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/logs"
)

// TestClassifyLogReadError pins the HIGH-3 fix: only a genuine absence maps to
// domain.ErrNotFound (→ 404 / "no logs"); every other sink error — the ones an
// object store surfaces (throttling, 5xx, denied creds, wrong region, missing
// bucket) — must propagate so the API returns 5xx, never a misleading 200.
func TestClassifyLogReadError(t *testing.T) {
	t.Run("object not-found maps to ErrNotFound", func(t *testing.T) {
		got := classifyLogReadError(fmt.Errorf("reading log object: %w", logs.ErrObjectNotFound))
		if !errors.Is(got, domain.ErrNotFound) {
			t.Fatalf("ErrObjectNotFound should map to domain.ErrNotFound, got %v", got)
		}
	})

	t.Run("disk not-found maps to ErrNotFound", func(t *testing.T) {
		got := classifyLogReadError(fmt.Errorf("opening log file: %w", os.ErrNotExist))
		if !errors.Is(got, domain.ErrNotFound) {
			t.Fatalf("os.ErrNotExist should map to domain.ErrNotFound, got %v", got)
		}
	})

	t.Run("transient store error does NOT map to ErrNotFound", func(t *testing.T) {
		transient := errors.New("getting log object: operation error S3: GetObject, https response error StatusCode: 503, SlowDown")
		got := classifyLogReadError(transient)
		if errors.Is(got, domain.ErrNotFound) {
			t.Fatal("a transient store error must NOT map to domain.ErrNotFound (would render as a misleading 200)")
		}
		if !errors.Is(got, transient) {
			t.Fatalf("the original error must be preserved for the 5xx path, got %v", got)
		}
	})

	t.Run("credential error does NOT map to ErrNotFound", func(t *testing.T) {
		credErr := errors.New("getting log object: no valid credential sources found")
		if errors.Is(classifyLogReadError(credErr), domain.ErrNotFound) {
			t.Fatal("a credential failure must surface as 5xx, not 200")
		}
	})
}
