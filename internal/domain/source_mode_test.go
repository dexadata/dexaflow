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

// Dispatch re-checks the cap with the same rule and message as register, for a
// version registered before source mode was turned on (ADR 0067 §3).
func TestCheckSourceModeSize(t *testing.T) {
	atCap := strings.Repeat("x", MaxSourceModeBytes)
	if err := CheckSourceModeSize("etl", atCap); err != nil {
		t.Errorf("CheckSourceModeSize at the cap = %v, want nil", err)
	}
	err := CheckSourceModeSize("etl", atCap+"x")
	if err == nil || !strings.Contains(err.Error(), "131073 bytes") || !strings.Contains(err.Error(), `"etl"`) {
		t.Errorf("CheckSourceModeSize over the cap = %v, want an error naming the DAG and 131073 bytes", err)
	}
	regErr := CheckSourceMode("rt", &DAGSpec{DagID: "etl", Image: "rt", Source: atCap + "x"})
	if regErr == nil || regErr.Error() != err.Error() {
		t.Errorf("register error %v and dispatch error %v differ", regErr, err)
	}
}

// The runtime image must carry a full sha256 digest: "@sha256:" and exactly 64
// lowercase hex characters at the end of the reference.
func TestIsDigestPinned(t *testing.T) {
	hex64 := strings.Repeat("0123456789abcdef", 4)
	cases := map[string]bool{
		"ghcr.io/dexadata/runtime@sha256:" + hex64:       true,
		"ghcr.io/dexadata/runtime:0.5.3@sha256:" + hex64: true,
		"rt@sha256:abc":                       false,
		"rt@sha256:":                          false,
		"rt@sha256:" + strings.ToUpper(hex64): false,
		"rt@sha256:" + hex64 + "0":            false,
		"rt@sha256:" + hex64 + ":latest":      false,
		"@sha256:" + hex64:                    false,
		"ghcr.io/dexadata/runtime:0.5.3":      false,
		"":                                    false,
	}
	for image, want := range cases {
		if got := IsDigestPinned(image); got != want {
			t.Errorf("IsDigestPinned(%q) = %v, want %v", image, got, want)
		}
	}
}
