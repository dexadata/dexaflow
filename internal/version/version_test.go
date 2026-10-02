package version

import (
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestGetReturnsLinkTimeDefaults(t *testing.T) {
	got := Get()
	if got.Version != "dev" {
		t.Errorf("Version = %q, want %q", got.Version, "dev")
	}
	if got.GitCommit != "none" {
		t.Errorf("GitCommit = %q, want %q", got.GitCommit, "none")
	}
	if got.BuildDate != "unknown" {
		t.Errorf("BuildDate = %q, want %q", got.BuildDate, "unknown")
	}
	if got.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", got.GoVersion, runtime.Version())
	}
}

func TestInfoStringContainsEveryField(t *testing.T) {
	info := Info{
		Version:   "v1.2.3",
		GitCommit: "abc1234",
		BuildDate: "2026-05-21T00:00:00Z",
		GoVersion: "go1.26.1",
	}
	s := info.String()
	for _, want := range []string{info.Version, info.GitCommit, info.BuildDate, info.GoVersion} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}

// TestInfoCarriesNoBuildExpiry pins the alpha-cut decision (this commit) that
// Dexaflow binaries do NOT carry a baked-in expiry. The pre-alpha 90-day timer
// was removed when the alpha was about to ship — re-introducing it would
// silently brick old installs after 90 days, which is exactly what we
// stopped doing on purpose. This test fails the build if any of the three
// removal signals regress:
//   - a struct field named "Expires*" reappears on Info
//   - the JSON shape grows an "expires_at" key
//   - the human-readable String() leaks the word "expires" / "expired"
func TestInfoCarriesNoBuildExpiry(t *testing.T) {
	rt := reflect.TypeOf(Info{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if strings.HasPrefix(strings.ToLower(name), "expire") {
			t.Errorf("Info field %q reintroduces a build expiry — see commit removing it", name)
		}
	}

	raw, err := json.Marshal(Info{Version: "v1.2.3", GitCommit: "c", BuildDate: "b", GoVersion: "g"})
	if err != nil {
		t.Fatalf("marshal Info: %v", err)
	}
	if strings.Contains(string(raw), "expires") {
		t.Errorf("Info JSON %q leaks an expires* key — the binary is meant to be timeless", string(raw))
	}

	s := Info{Version: "v1.2.3", GitCommit: "c", BuildDate: "b", GoVersion: "g"}.String()
	lower := strings.ToLower(s)
	if strings.Contains(lower, "expires") || strings.Contains(lower, "expired") {
		t.Errorf("Info.String() %q hints at expiry — the binary is meant to be timeless", s)
	}
}
