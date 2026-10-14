package domain

import (
	"strings"
	"testing"
)

const pinnedImage = "ghcr.io/acme/etl@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// deploy.require_digest (#1703): off accepts every image, warn accepts an
// unpinned one with a warning, enforce refuses it. A pinned image passes
// every mode with nothing to say.
func TestDigestRequirementCheckImage(t *testing.T) {
	cases := map[string]struct {
		mode     DigestRequirement
		image    string
		wantWarn bool
		wantErr  bool
	}{
		"off, tag":           {DigestRequireOff, "ghcr.io/acme/etl:v1", false, false},
		"empty mode, tag":    {"", "ghcr.io/acme/etl:v1", false, false},
		"warn, tag":          {DigestRequireWarn, "ghcr.io/acme/etl:v1", true, false},
		"warn, pinned":       {DigestRequireWarn, pinnedImage, false, false},
		"enforce, tag":       {DigestRequireEnforce, "ghcr.io/acme/etl:v1", false, true},
		"enforce, no tag":    {DigestRequireEnforce, "ghcr.io/acme/etl", false, true},
		"enforce, tag+short": {DigestRequireEnforce, "ghcr.io/acme/etl:v1@sha256:abc", false, true},
		"enforce, pinned":    {DigestRequireEnforce, pinnedImage, false, false},
		"enforce, tag+full":  {DigestRequireEnforce, "ghcr.io/acme/etl:v1@sha256:" + strings.Repeat("a", 64), false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			warning, err := tc.mode.CheckImage(tc.image)
			if (warning != "") != tc.wantWarn {
				t.Errorf("warning = %q, want one: %v", warning, tc.wantWarn)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want one: %v", err, tc.wantErr)
			}
		})
	}
}

// The warning and the refusal name the setting, the image and how to pin it,
// so the author can act on either without reading the server's docs.
func TestDigestRequirementMessagesNameSettingAndFix(t *testing.T) {
	const img = "ghcr.io/acme/etl:v1"
	warning, _ := DigestRequireWarn.CheckImage(img)
	_, err := DigestRequireEnforce.CheckImage(img)
	if err == nil {
		t.Fatal("enforce accepted a tag")
	}
	for name, msg := range map[string]string{"warning": warning, "refusal": err.Error()} {
		for _, want := range []string{"deploy.require_digest", img, "@sha256:", "dexaflow deploy"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s %q does not name %q", name, msg, want)
			}
		}
	}
}

func TestParseDigestRequirement(t *testing.T) {
	for in, want := range map[string]DigestRequirement{
		"": DigestRequireOff, "off": DigestRequireOff, "warn": DigestRequireWarn, "enforce": DigestRequireEnforce,
	} {
		got, err := ParseDigestRequirement(in)
		if err != nil || got != want {
			t.Errorf("ParseDigestRequirement(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"refuse", "on", "true", "Warn", " warn"} {
		if _, err := ParseDigestRequirement(in); err == nil || !strings.Contains(err.Error(), "deploy.require_digest") {
			t.Errorf("ParseDigestRequirement(%q) err = %v, want one naming deploy.require_digest", in, err)
		}
	}
}
