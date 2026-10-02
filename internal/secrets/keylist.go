package secrets

import (
	"fmt"
	"strings"
)

// ParseKeys parses a comma-separated key list. The FIRST key encrypts and
// decrypts; every later one only decrypts.
//
// This is the shape Apache Airflow's `fernet_key` uses, and Dexaflow is an
// Airflow-compatible control plane, so an operator arriving from Airflow
// already knows the rule: put the new key first, the old ones after, then run
// the re-encryption and drop the tail.
//
// A single key parses to exactly what ParseKey returns for it, so every
// deployment that predates the list keeps working unchanged.
//
// A malformed entry is an error rather than a silent drop. Dropping it would
// leave an operator believing a predecessor is in the read set when it is not,
// and the next thing they do is delete the only remaining copy of it.
func ParseKeys(s string) ([][]byte, error) {
	parts := strings.Split(s, ",")
	keys := make([][]byte, 0, len(parts))
	for i, raw := range parts {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		k, err := ParseKey(trimmed)
		if err != nil {
			return nil, fmt.Errorf("key %d of %d: %w", i+1, len(parts), err)
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no keys given")
	}
	return keys, nil
}
