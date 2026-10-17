package oidc

import (
	"errors"
	"testing"
	"time"
)

func TestStateCodecRoundTrip(t *testing.T) {
	c := NewStateCodec("app-secret")
	in := StatePayload{State: "st-123", Nonce: "nonce-abc", Verifier: "vfy-xyz", Next: "/dags"}
	tok, err := c.Encode(in, time.Minute)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(tok)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != in {
		t.Errorf("round-trip = %+v, want %+v", got, in)
	}
}

func TestStateCodecRejectsTamper(t *testing.T) {
	c := NewStateCodec("app-secret")
	tok, err := c.Encode(StatePayload{State: "st", Nonce: "n", Verifier: "v"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt a byte in the middle of the token (the payload segment) so the
	// signed content no longer matches the signature.
	b := []byte(tok)
	mid := len(b) / 2
	if b[mid] == 'a' {
		b[mid] = 'b'
	} else {
		b[mid] = 'a'
	}
	if _, err := c.Decode(string(b)); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Decode(tampered) err = %v, want ErrInvalidState", err)
	}
}

func TestStateCodecRejectsWrongKey(t *testing.T) {
	tok, err := NewStateCodec("secret-one").Encode(StatePayload{State: "st"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A codec derived from a different app secret must not validate the cookie —
	// this is what stops a state cookie and a session token from cross-verifying.
	if _, err := NewStateCodec("secret-two").Decode(tok); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Decode(wrong key) err = %v, want ErrInvalidState", err)
	}
}

func TestStateCodecRejectsExpired(t *testing.T) {
	c := NewStateCodec("app-secret")
	tok, err := c.Encode(StatePayload{State: "st"}, -time.Second) // already expired
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Decode(tok); !errors.Is(err, ErrInvalidState) {
		t.Errorf("Decode(expired) err = %v, want ErrInvalidState", err)
	}
}

func TestStateCodecRoundTripsCLIBinding(t *testing.T) {
	c := NewStateCodec("app-secret")
	in := StatePayload{
		State: "st", Nonce: "n", Verifier: "v", Next: "/",
		CLI: &CLIBinding{
			Redirect:  "http://127.0.0.1:43123/callback",
			State:     "cli-state-123",
			Challenge: "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM",
		},
	}
	tok, err := c.Encode(in, time.Minute)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(tok)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.CLI == nil || *got.CLI != *in.CLI {
		t.Fatalf("CLI binding = %+v, want %+v", got.CLI, in.CLI)
	}
}

func TestStateCodecBrowserLoginHasNoCLIBinding(t *testing.T) {
	c := NewStateCodec("app-secret")
	tok, err := c.Encode(StatePayload{State: "st", Nonce: "n", Verifier: "v", Next: "/"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Decode(tok)
	if err != nil {
		t.Fatal(err)
	}
	if got.CLI != nil {
		t.Errorf("a browser login decoded with a CLI binding: %+v", got.CLI)
	}
}

func TestStateCodecRoundTripsDeviceBinding(t *testing.T) {
	c := NewStateCodec("app-secret")
	in := StatePayload{
		State: "st", Nonce: "n", Verifier: "v", Next: "/",
		Device: &DeviceBinding{UserCode: "BCDF-GHJK"},
	}
	tok, err := c.Encode(in, time.Minute)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(tok)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Device == nil || *got.Device != *in.Device {
		t.Fatalf("device binding = %+v, want %+v", got.Device, in.Device)
	}
	if got.CLI != nil {
		t.Errorf("a device login decoded with a loopback binding: %+v", got.CLI)
	}
}
