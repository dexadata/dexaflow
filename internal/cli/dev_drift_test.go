package cli

import (
	"strings"
	"testing"
)

// TestDecideSchemaDrift pins the drift detector's decision (#136). The startup
// path of `dexaflow lite` relies on this to refuse to run an older binary
// against a database a newer binary has already upgraded — the alternative
// (silent reads/writes under a stale schema) is data corruption.
func TestDecideSchemaDrift(t *testing.T) {
	tests := []struct {
		name      string
		dbVersion uint
		dirty     bool
		embedded  uint
		wantErr   string // substring; empty = no error expected
	}{
		{
			name:      "DB at the same version as the binary is fine",
			dbVersion: 15, dirty: false, embedded: 15, wantErr: "",
		},
		{
			name:      "DB older than the binary is fine (m.Up() will catch up)",
			dbVersion: 10, dirty: false, embedded: 15, wantErr: "",
		},
		{
			name:      "DB ahead of the binary is the drift case — refuse",
			dbVersion: 20, dirty: false, embedded: 15,
			wantErr: "older `dexaflow` is being run against a newer database",
		},
		{
			name:      "DB just one version ahead is still drift",
			dbVersion: 16, dirty: false, embedded: 15,
			wantErr: "schema version 16",
		},
		{
			name:      "dirty marker is a separate operational failure (not silent drift)",
			dbVersion: 15, dirty: true, embedded: 15,
			wantErr: "marked dirty at version 15",
		},
		{
			name:      "dirty wins over the drift check (both bad, dirty is the more actionable one)",
			dbVersion: 20, dirty: true, embedded: 15,
			wantErr: "marked dirty at version 20",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := decideSchemaDrift(tc.dbVersion, tc.dirty, tc.embedded)
			if tc.wantErr == "" {
				if err != nil {
					t.Errorf("decideSchemaDrift = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Errorf("decideSchemaDrift = nil, want error containing %q", tc.wantErr)
				return
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("decideSchemaDrift err = %v, want substring %q", err, tc.wantErr)
			}
		})
	}
}
