package main

import (
	"strings"
	"testing"
)

// `-version` is what an operator pastes into a bug report, and the image used
// to print golang-migrate's version alone: keep both the Dexaflow build and the
// golang-migrate version it compiles in.
func TestVersionStringNamesBothBuilds(t *testing.T) {
	got := versionString()
	for _, want := range []string{"dexaflow-migrate ", "golang-migrate v4."} {
		if !strings.Contains(got, want) {
			t.Errorf("versionString() = %q, lacks %q", got, want)
		}
	}
}
