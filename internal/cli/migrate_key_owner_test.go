package cli

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/storage"
)

// migrate-key names what an unreadable value belongs to, and encrypted
// Variables are in the registry since #507.
func TestSecretOwnerNamesEveryRegisteredTable(t *testing.T) {
	t.Parallel()
	want := map[string]string{"connections": "connection", "variables": "variable"}
	for _, c := range storage.EncryptedColumns() {
		if got := secretOwner(c); got != want[c.Table] {
			t.Errorf("secretOwner(%s) = %q, want %q", c, got, want[c.Table])
		}
	}
}
