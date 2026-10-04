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

// TestTryEpochs pins the epoch range a try's log read probes, including a try
// the database has no row for: an API client can name a try above the task's
// current one while earlier tries archived non-zero epochs, so the upper bound
// is below the lower one. That must read as epoch 0 alone (a 404), not panic.
func TestTryEpochs(t *testing.T) {
	cases := []struct {
		name      string
		low, high int
		want      []int
	}{
		{"legacy try", 0, 0, []int{0}},
		{"one post-upgrade try", 0, 3, []int{0, 1, 2, 3}},
		{"later try", 3, 5, []int{0, 4, 5}},
		{"unknown try above earlier archived epochs", 7, 0, []int{0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tryEpochs(tc.low, tc.high)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("tryEpochs(%d, %d) = %v, want %v", tc.low, tc.high, got, tc.want)
			}
		})
	}
	if got := tryEpochs(0, 1000); len(got) != maxTryLogStreams+1 || got[0] != 0 || got[1] != 1000-maxTryLogStreams+1 {
		t.Fatalf("a capped range keeps epoch 0 and the latest %d epochs, got len %d starting %v", maxTryLogStreams, len(got), got[:2])
	}
}
