package version

import (
	"strings"
	"testing"
)

func TestPromoted(t *testing.T) {
	cases := []struct {
		name, linked, release, want string
	}{
		{"rc to its own GA", "0.6.0-rc.2", "v0.6.0", "0.6.0"},
		{"rc to its own GA without v", "0.6.0-rc.2", "0.6.0", "0.6.0"},
		{"v-prefixed rc keeps the v", "v0.6.0-rc.10", "v0.6.0", "v0.6.0"},
		{"no variable", "0.6.0-rc.2", "", "0.6.0-rc.2"},
		{"another release", "0.6.0-rc.2", "v0.6.1", "0.6.0-rc.2"},
		{"an older release", "0.6.0-rc.2", "v0.5.12", "0.6.0-rc.2"},
		{"another rc", "0.6.0-rc.2", "v0.6.0-rc.3", "0.6.0-rc.2"},
		{"GA build ignores it", "0.6.0", "v0.6.0", "0.6.0"},
		{"GA build cannot be renamed", "0.6.0", "v1.0.0", "0.6.0"},
		{"dev build ignores it", "dev", "v0.6.0", "dev"},
		{"prerelease that is not an rc", "0.6.0-beta.1", "v0.6.0", "0.6.0-beta.1"},
		{"suffix after the rc", "0.6.0-rc.2+dirty", "v0.6.0", "0.6.0-rc.2+dirty"},
		{"padding in the variable", "0.6.0-rc.2", " v0.6.0", "0.6.0-rc.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promoted(tc.linked, tc.release); got != tc.want {
				t.Errorf("promoted(%q, %q) = %q, want %q", tc.linked, tc.release, got, tc.want)
			}
		})
	}
}

func TestGetReportsPromotedReleaseAndBuiltAs(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "0.6.0-rc.2"

	t.Setenv(ReleaseVersionEnv, "v0.6.0")
	got := Get()
	if got.Version != "0.6.0" || got.BuiltAs != "0.6.0-rc.2" {
		t.Fatalf("Get() = %+v, want Version 0.6.0 built as 0.6.0-rc.2", got)
	}
	if s := got.String(); !strings.Contains(s, "dexaflow 0.6.0 (built as 0.6.0-rc.2,") {
		t.Errorf("String() = %q, want the release and the version it was built as", s)
	}
	if Promoted(version) != "0.6.0" {
		t.Errorf("Promoted(%q) = %q, want 0.6.0", version, Promoted(version))
	}

	t.Setenv(ReleaseVersionEnv, "v0.7.0")
	got = Get()
	if got.Version != "0.6.0-rc.2" || got.BuiltAs != "" {
		t.Errorf("Get() with another release = %+v, want the rc unchanged", got)
	}
	if s := got.String(); strings.Contains(s, "built as") {
		t.Errorf("String() = %q mentions built as without a promotion", s)
	}
}
