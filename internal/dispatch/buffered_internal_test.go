package dispatch

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
)

// TestNewBuffered_DefaultMaxWaitIsHalfTheDispatchLostThreshold: the server
// wires the buffer without a MaxWait, so the default is what production runs
// with. Half the dispatch-lost threshold leaves the other half for the create
// itself and for the reaper's sweep cadence (#1607).
func TestNewBuffered_DefaultMaxWaitIsHalfTheDispatchLostThreshold(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero selects the default", 0, executor.DefaultReaperConfig().DispatchLostThreshold / 2},
		{"positive is kept", 5 * time.Second, 5 * time.Second},
		{"negative disables the bound", -1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuffered(nil, nil, logger, nil, BufferConfig{BufferSize: 1, Workers: 1, MaxWait: tc.in})
			defer func() { _ = b.Close() }()
			if b.maxWait != tc.want {
				t.Errorf("maxWait = %s, want %s", b.maxWait, tc.want)
			}
		})
	}
}
