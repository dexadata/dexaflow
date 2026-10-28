package storage

import (
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
)

func variableTestCipher(t *testing.T, seed byte) secrets.StaleReader {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	c, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	return secrets.WithFallback(c)
}

func TestParseVariableEncryption(t *testing.T) {
	for in, want := range map[string]VariableEncryption{
		"": VariableEncryptionOff, "off": VariableEncryptionOff,
		"on": VariableEncryptionOn, "decrypt": VariableEncryptionDecrypt,
	} {
		got, err := ParseVariableEncryption(in)
		if err != nil || got != want {
			t.Errorf("ParseVariableEncryption(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseVariableEncryption("yes"); err == nil {
		t.Error("an unknown mode must be rejected, not read as off")
	}
}

// The default (off) keeps today's stored form byte for byte: a v0.5.3 replica
// reading the same row sees exactly what it wrote.
func TestSealVariableOffStoresPlaintext(t *testing.T) {
	r := &Repository{cipher: variableTestCipher(t, 1)}
	got, err := r.sealVariable("postgres://u:p@db/x")
	if err != nil || got != "postgres://u:p@db/x" {
		t.Fatalf("off mode must store the value as is, got %q, %v", got, err)
	}
	// Without a key the default still works, as it does today.
	r = &Repository{}
	if got, err := r.sealVariable("v"); err != nil || got != "v" {
		t.Fatalf("off mode without a key: %q, %v", got, err)
	}
}

func TestSealVariableOnEncrypts(t *testing.T) {
	r := &Repository{cipher: variableTestCipher(t, 1), varEncryption: VariableEncryptionOn}
	stored, err := r.sealVariable("sk_live_123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, VariableCiphertextPrefix) || strings.Contains(stored, "sk_live_123") {
		t.Fatalf("on mode must store tagged ciphertext, got %q", stored)
	}
	plain, enc, err := r.openVariable(stored)
	if err != nil || !enc || plain != "sk_live_123" {
		t.Fatalf("openVariable = %q, %v, %v", plain, enc, err)
	}
}

// Encryption is explicit: with no key there is nothing to encrypt with, and
// storing plaintext under "on" would be a silent downgrade (ADR 0019).
func TestSealVariableOnWithoutKeyRefuses(t *testing.T) {
	r := &Repository{varEncryption: VariableEncryptionOn}
	if _, err := r.sealVariable("x"); !errors.Is(err, secrets.ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
}

// A plaintext value that starts with the ciphertext tag would read back as
// ciphertext, so it is refused as invalid input in every mode.
func TestSealVariableRejectsTheReservedPrefix(t *testing.T) {
	for _, mode := range []VariableEncryption{VariableEncryptionOff, VariableEncryptionOn, VariableEncryptionDecrypt} {
		r := &Repository{cipher: variableTestCipher(t, 1), varEncryption: mode}
		if _, err := r.sealVariable(VariableCiphertextPrefix + "abc"); !errors.Is(err, domain.ErrValidation) {
			t.Errorf("%s: want ErrValidation, got %v", mode, err)
		}
	}
}

func TestOpenVariableLegacyPlaintext(t *testing.T) {
	for _, r := range []*Repository{{}, {cipher: variableTestCipher(t, 1), varEncryption: VariableEncryptionOn}} {
		plain, enc, err := r.openVariable("plain value")
		if err != nil || enc || plain != "plain value" {
			t.Fatalf("a legacy plaintext row must read as is: %q, %v, %v", plain, enc, err)
		}
	}
}

func TestOpenVariableEncryptedNeedsTheKey(t *testing.T) {
	writer := &Repository{cipher: variableTestCipher(t, 1), varEncryption: VariableEncryptionOn}
	stored, err := writer.sealVariable("v")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Repository{}).openVariable(stored); !errors.Is(err, secrets.ErrNoKey) {
		t.Errorf("no key: want ErrNoKey, got %v", err)
	}
	// Off mode still reads what an earlier "on" wrote: turning encryption off
	// never makes a stored value unreadable.
	off := &Repository{cipher: variableTestCipher(t, 1)}
	if plain, enc, err := off.openVariable(stored); err != nil || !enc || plain != "v" {
		t.Errorf("off mode read: %q, %v, %v", plain, enc, err)
	}
	wrong := &Repository{cipher: variableTestCipher(t, 99)}
	if _, _, err := wrong.openVariable(stored); err == nil {
		t.Error("a wrong key must fail, never return the ciphertext as the value")
	}
}

// A listing marks a value no key opens instead of failing (#507): no value,
// never the ciphertext, and the description kept. Reading it alone is an
// ErrValueUnreadable error naming the key.
func TestListVariableMarksAnUnreadableValue(t *testing.T) {
	writer := &Repository{cipher: variableTestCipher(t, 1), varEncryption: VariableEncryptionOn}
	stored, err := writer.sealVariable("v")
	if err != nil {
		t.Fatal(err)
	}
	desc := "a note"
	wrong := &Repository{cipher: variableTestCipher(t, 99)}
	got := wrong.listVariable("lost", stored, &desc)
	want := domain.Variable{Key: "lost", Description: "a note", Encrypted: true, Unreadable: true}
	if got != want {
		t.Errorf("listVariable = %+v, want %+v", got, want)
	}
	if _, err := wrong.toVariable("lost", stored, &desc); !errors.Is(err, domain.ErrValueUnreadable) || !strings.Contains(err.Error(), `"lost"`) {
		t.Errorf("toVariable = %v, want ErrValueUnreadable naming the key", err)
	}
	if got := writer.listVariable("ok", stored, nil); got.Unreadable || got.Value != "v" {
		t.Errorf("readable listVariable = %+v", got)
	}
}
