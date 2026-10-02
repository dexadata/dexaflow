package domain

import (
	"errors"
	"fmt"
	"testing"
)

// TestSafefKeepsTheSentinelClass pins the property every caller relies on: a
// Safef error is still the sentinel it was built from, so the API's status
// mapping is unchanged by the switch away from fmt.Errorf.
func TestSafefKeepsTheSentinelClass(t *testing.T) {
	err := Safef(ErrValidation, "unknown role %q", "analyst")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Safef(ErrValidation, ...) is not ErrValidation: %v", err)
	}
	if errors.Is(err, ErrConflict) {
		t.Error("Safef must not match a class it was not built with")
	}
	// Wrapping is the normal shape at the call sites, so it must survive one.
	wrapped := fmt.Errorf("creating user: %w", err)
	if !errors.Is(wrapped, ErrValidation) {
		t.Error("wrapping a Safef loses its class")
	}
}

// TestSafefErrorTextMatchesTheOldWrapping keeps logs and existing string
// assertions stable: "<message>: <class>" is exactly what
// fmt.Errorf("<message>: %w", class) produced.
func TestSafefErrorTextMatchesTheOldWrapping(t *testing.T) {
	got := Safef(ErrValidation, "unknown role %q", "analyst").Error()
	want := fmt.Errorf("unknown role %q: %w", "analyst", ErrValidation).Error()
	if got != want {
		t.Errorf("Safef text = %q, want %q", got, want)
	}
}

// TestClientMessageIsTheComposedTextOnly is what the API boundary reads: the
// message Dexaflow composed, without the sentinel's own wording appended.
func TestClientMessageIsTheComposedTextOnly(t *testing.T) {
	var se *SafeError
	if !errors.As(Safef(ErrConflict, "dag %q is at max_active_runs cap of %d", "etl", 3), &se) {
		t.Fatal("Safef must be reachable with errors.As(*SafeError)")
	}
	if se.ClientMessage() != `dag "etl" is at max_active_runs cap of 3` {
		t.Errorf("ClientMessage = %q", se.ClientMessage())
	}
}
