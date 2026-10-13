package build

import (
	"strings"
	"testing"
)

func TestParsePlatforms(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"linux/amd64"}, "linux/amd64"},
		{[]string{"linux/amd64,linux/arm64"}, "linux/amd64,linux/arm64"},
		{[]string{"linux/amd64", " linux/arm64/v8 "}, "linux/amd64,linux/arm64/v8"},
		// A repeated value is one platform, not a multi-platform request.
		{[]string{"linux/amd64", "linux/amd64"}, "linux/amd64"},
		// docker matches platforms without regard to case; refusing one that
		// works today would break a project that builds on main.
		{[]string{"Linux/AMD64"}, "Linux/AMD64"},
	}
	for _, c := range cases {
		got, err := ParsePlatforms(c.in)
		if err != nil {
			t.Errorf("ParsePlatforms(%q): %v", c.in, err)
			continue
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("ParsePlatforms(%q) = %v, want %s", c.in, got, c.want)
		}
	}
}

func TestParsePlatformsRefusesMalformedValues(t *testing.T) {
	for _, in := range []string{"", "amd64", "linux/", "/amd64", "linux/amd64/v8/x", "linux/amd 64", "linux/amd64;rm", "linux/amd64,"} {
		_, err := ParsePlatforms([]string{in})
		if err == nil {
			t.Errorf("ParsePlatforms(%q) accepted a malformed platform", in)
			continue
		}
		if !strings.Contains(err.Error(), "os/arch") {
			t.Errorf("error %q does not say the expected form", err)
		}
	}
	if _, err := ParsePlatforms(nil); err == nil {
		t.Error("an empty list must be refused: a flag given with no value selects nothing")
	}
}
