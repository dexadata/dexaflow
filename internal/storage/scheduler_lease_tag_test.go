package storage

import "testing"

// TestSessionTermTagNeverMatchesWithoutATerm: a replica that never led tags
// its sessions with a value no lease term equals, so its writes are refused
// even on a database whose term is still 0 (ADR 0106).
func TestSessionTermTagNeverMatchesWithoutATerm(t *testing.T) {
	if got := sessionTermTag(0); got != "-1" {
		t.Fatalf("tag without a term = %q, want -1", got)
	}
	if got := sessionTermTag(42); got != "42" {
		t.Fatalf("tag of term 42 = %q, want 42", got)
	}
}
