package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	dexaflow "github.com/dexadata/dexaflow"
)

// managedComposeName is the compose Lite materializes under ~/.dexaflow for a
// binary-only install.
const managedComposeName = "docker-compose.yaml"

// managedComposeMarker holds the SHA-256 of the compose a binary last wrote
// beside it, so a later binary can tell an untouched copy (safe to replace)
// from one the operator edited (#981).
const managedComposeMarker = managedComposeName + ".sha256"

// composeChecksum is the hex SHA-256 recorded in managedComposeMarker.
func composeChecksum(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ensureManagedComposeIn keeps dir's managed compose in step with the one this
// binary embeds, the way ensurePysrcIn keeps the bundled parser (#981). It
// writes the compose when absent; replaces it, and says so, when it still
// matches the checksum a previous binary recorded (an untouched copy from an
// older install); and otherwise keeps the operator's file as it is, writes
// this binary's compose beside it as <name>.new, and warns once per new
// compose. A file without a marker was written before the checksum existed:
// it is adopted silently when it equals the embed, and kept as an edited file
// otherwise, since nothing proves it is untouched. It returns the compose path.
func ensureManagedComposeIn(dir string, logf func(format string, args ...any)) (string, error) {
	embed := dexaflow.DevCompose()
	want := composeChecksum(embed)
	path := filepath.Join(dir, managedComposeName)
	marker := filepath.Join(dir, managedComposeMarker)
	newPath := path + ".new"

	cur, err := os.ReadFile(path) //nolint:gosec // path derived from the per-user Dexaflow home
	if errors.Is(err, os.ErrNotExist) {
		if wErr := writeFileAtomic(path, embed); wErr != nil {
			return "", fmt.Errorf("writing managed compose %s: %w", path, wErr)
		}
		recordComposeChecksum(marker, want)
		_ = os.Remove(newPath) //nolint:errcheck // a stale side copy is harmless
		return path, nil
	}
	if err != nil {
		return "", fmt.Errorf("reading managed compose %s: %w", path, err)
	}
	have := composeChecksum(cur)
	recorded, _ := os.ReadFile(marker) //nolint:errcheck,gosec // an absent marker means "written before the checksum existed"
	switch {
	case have == want:
		if strings.TrimSpace(string(recorded)) != want {
			recordComposeChecksum(marker, want)
		}
		_ = os.Remove(newPath) //nolint:errcheck // the operator merged it; the side copy is no longer needed
	case len(recorded) > 0 && have == strings.TrimSpace(string(recorded)):
		if wErr := writeFileAtomic(path, embed); wErr != nil {
			return "", fmt.Errorf("refreshing managed compose %s: %w", path, wErr)
		}
		recordComposeChecksum(marker, want)
		logf("↻ refreshed the managed compose (binary upgrade): %s", path)
	default:
		if prev, _ := os.ReadFile(newPath); bytes.Equal(prev, embed) { //nolint:errcheck,gosec // absent means not warned yet
			return path, nil
		}
		if wErr := writeFileAtomic(newPath, embed); wErr != nil {
			return "", fmt.Errorf("writing the new managed compose %s: %w", newPath, wErr)
		}
		logf("⚠ %s differs from the compose this dexaflow ships (it was edited, or written by an older dexaflow), so it was kept as is. "+
			"The new one is at %s: merge what you need, or delete %s and the next `dexaflow lite` writes the new one.", path, newPath, path)
	}
	return path, nil
}

// recordComposeChecksum writes the marker. A failed write only means the next
// start cannot prove the file untouched and keeps it, so it is not fatal.
func recordComposeChecksum(marker, sum string) {
	_ = os.WriteFile(marker, []byte(sum+"\n"), 0o600) //nolint:errcheck // see the doc comment
}
