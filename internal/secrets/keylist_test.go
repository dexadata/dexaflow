package secrets_test

import (
	"bytes"
	"testing"

	"github.com/neochaotic/leoflow/internal/secrets"
)

// LEOFLOW_SECRET_KEY takes a comma-separated list: the first key encrypts and
// decrypts, the rest only decrypt. It is the shape Airflow's fernet_key uses,
// so an operator arriving from Airflow already knows to put the new key first
// and the old ones after (#486).
func TestParseKeysSplitsAndOrders(t *testing.T) {
	primary := "11111111111111111111111111111111"
	older := "22222222222222222222222222222222"
	oldest := "33333333333333333333333333333333"

	keys, err := secrets.ParseKeys(primary + "," + older + "," + oldest)
	if err != nil {
		t.Fatalf("ParseKeys: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("got %d keys, want 3: a rotation must be able to carry more than one predecessor", len(keys))
	}
	if string(keys[0]) != primary {
		t.Errorf("keys[0] must be the ENCRYPTING key; got %q", keys[0])
	}
}

// Whitespace around entries is the operator writing the list readably, not a
// different key.
func TestParseKeysToleratesSpacingAndEmptyEntries(t *testing.T) {
	keys, err := secrets.ParseKeys("  11111111111111111111111111111111 , , 22222222222222222222222222222222  ")
	if err != nil {
		t.Fatalf("ParseKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %d keys, want 2 (blank entries dropped)", len(keys))
	}
}

// A single key is the overwhelmingly common case and must keep working
// byte-for-byte as before, or this change breaks every existing deployment.
func TestParseKeysAcceptsASingleKeyUnchanged(t *testing.T) {
	one := "11111111111111111111111111111111"
	keys, err := secrets.ParseKeys(one)
	if err != nil {
		t.Fatalf("ParseKeys: %v", err)
	}
	if len(keys) != 1 || string(keys[0]) != one {
		t.Errorf("a single key must parse to exactly itself, got %q", keys)
	}
	single, serr := secrets.ParseKey(one)
	if serr != nil || !bytes.Equal(single, keys[0]) {
		t.Errorf("ParseKeys must agree with ParseKey on a single key")
	}
}

// A bad entry anywhere must fail loudly. Silently dropping it would leave the
// operator believing an old key is in the read set when it is not, and they
// would then delete the only copy of it.
func TestParseKeysRefusesABadEntry(t *testing.T) {
	if _, err := secrets.ParseKeys("11111111111111111111111111111111,too-short"); err == nil {
		t.Error("a malformed key in the list must be an error, not a silent drop")
	}
}

func TestParseKeysRefusesEmpty(t *testing.T) {
	if _, err := secrets.ParseKeys("   "); err == nil {
		t.Error("an empty list must be an error so the caller can disable encryption explicitly")
	}
}
