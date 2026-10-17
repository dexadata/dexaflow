package storage

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestBuildInfoOfLogsAnUndecodableValue: a build_info that does not decode
// leaves the version listable without build info, and the decode error is
// logged with the version it belongs to, never dropped silently.
func TestBuildInfoOfLogsAnUndecodableValue(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	if got := buildInfoOf("v-1", []byte(`{"commit": 7`)); got != nil {
		t.Errorf("buildInfoOf(bad) = %+v, want nil", got)
	}
	log := buf.String()
	for _, want := range []string{`"level":"WARN"`, `"dag_version_id":"v-1"`, `"error":`} {
		if !strings.Contains(log, want) {
			t.Errorf("log %q misses %s", log, want)
		}
	}
	buf.Reset()
	if got := buildInfoOf("v-2", nil); got != nil || buf.Len() != 0 {
		t.Errorf("buildInfoOf(NULL) = %+v, log %q: want nil and no log", got, buf.String())
	}
	if got := buildInfoOf("v-3", []byte(`{"commit":"abc1234"}`)); got == nil || got.Commit != "abc1234" || buf.Len() != 0 {
		t.Errorf("buildInfoOf(valid) = %+v, log %q", got, buf.String())
	}
}
