package domain

import (
	"strings"
	"testing"
)

// ADR 0067 §3: a version runs in source mode when the mode is on (its runtime
// image is set), the version's image is that runtime image, and it carries a
// source. Anything else runs exactly as before.
func TestSourceModeApplies(t *testing.T) {
	const rt = "ghcr.io/dexadata/runtime@sha256:abc"
	cases := map[string]struct {
		mode, image string
		hasSource   bool
		want        bool
	}{
		"on, runtime image, source":  {rt, rt, true, true},
		"on, runtime image, no src":  {rt, rt, false, false},
		"on, own image, source":      {rt, "etl:v1", true, false},
		"off, source":                {"", rt, true, false},
		"off, empty image, source":   {"", "", true, false},
		"on, image is a tag of same": {rt, "ghcr.io/dexadata/runtime:latest", true, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SourceModeApplies(tc.mode, tc.image, tc.hasSource); got != tc.want {
				t.Errorf("SourceModeApplies(%q, %q, %v) = %v, want %v", tc.mode, tc.image, tc.hasSource, got, tc.want)
			}
		})
	}
}

func TestMaxSourceModeBytesIs128KiB(t *testing.T) {
	if MaxSourceModeBytes != 128*1024 {
		t.Errorf("MaxSourceModeBytes = %d, want 131072 (half the 256 KiB annotation budget)", MaxSourceModeBytes)
	}
}

// Register refuses a version on the runtime image whose source is empty or
// over the cap, and leaves every other version alone.
func TestCheckSourceMode(t *testing.T) {
	const rt = "ghcr.io/dexadata/runtime@sha256:abc"
	atCap := strings.Repeat("x", MaxSourceModeBytes)
	cases := map[string]struct {
		mode, image, source string
		wantErr             string
	}{
		"off ignores an empty source":       {"", rt, "", ""},
		"off ignores an empty image":        {"", "", "", ""},
		"off ignores an oversize source":    {"", rt, atCap + "x", ""},
		"own image with no source":          {rt, "etl:v1", "", ""},
		"own image with an oversize source": {rt, "etl:v1", atCap + "x", ""},
		"runtime image with a source":       {rt, rt, "print(1)\n", ""},
		"runtime image at the cap":          {rt, rt, atCap, ""},
		"runtime image with no source":      {rt, rt, "", "has no source"},
		"runtime image over the cap":        {rt, rt, atCap + "x", "131073 bytes"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := CheckSourceMode(tc.mode, &DAGSpec{DagID: "etl", Image: tc.image, Source: tc.source})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("CheckSourceMode = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("CheckSourceMode = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}
