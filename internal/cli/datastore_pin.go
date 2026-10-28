package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// datastorePinFile, under the Lite dev dir (~/.dexaflow/dev), records the
// Postgres backend this install is pinned to: written by `dexaflow lite restore`
// (managed, where the restored data lives) and by a run with an explicit
// --postgres. An "auto" run follows a managed pin instead of picking the Docker
// Postgres, so a restored install boots its data on a host with Docker too
// (ADR 0094, #1997). An install with no pin keeps the ADR 0030 rule.
const datastorePinFile = "datastore"

// readDatastorePin returns the pinned backend, or "" when there is none or the
// file holds anything other than a known backend.
func readDatastorePin(devDir string) string {
	b, err := os.ReadFile(filepath.Join(devDir, datastorePinFile)) //nolint:gosec // devDir is the per-user Lite state dir
	if err != nil {
		return ""
	}
	switch v := strings.TrimSpace(string(b)); v {
	case datastoreDocker, datastoreManaged:
		return v
	default:
		return ""
	}
}

// writeDatastorePin pins the install to mode (docker or managed).
func writeDatastorePin(devDir, mode string) error {
	if err := os.MkdirAll(devDir, 0o750); err != nil {
		return fmt.Errorf("creating dev state dir %s: %w", devDir, err)
	}
	p := filepath.Join(devDir, datastorePinFile)
	if err := os.WriteFile(p, []byte(mode+"\n"), 0o600); err != nil {
		return fmt.Errorf("recording the datastore in %s: %w", p, err)
	}
	return nil
}
