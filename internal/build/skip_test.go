package build

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fixedRegistry answers every Resolve with pinned, or with err.
type fixedRegistry struct {
	pinned   Pinned
	platform string
	err      error
	asked    []string
}

func (r *fixedRegistry) Resolve(_ context.Context, image string) (Pinned, error) {
	r.asked = append(r.asked, image)
	return r.pinned, r.err
}

// Platform answers with the platform the fixture names.
func (r *fixedRegistry) Platform(context.Context, string) (string, error) { return r.platform, nil }

const amd64Index = `{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"unknown","architecture":"unknown"}}]}`

// An image already in the registry under a content label is reused: the
// skip path, with the registry's digest and no build.
func TestSelectReusesTheImageForThisContent(t *testing.T) {
	reg := &fixedRegistry{pinned: Pinned{Ref: "reg.io/etl@sha256:abc", Manifest: []byte(amd64Index)}}
	b, reason := Select(context.Background(), Options{Reuse: true, Registry: reg}, "reg.io/etl:v1-abc", []string{"linux/amd64"})
	if b.Path() != PathSkip {
		t.Fatalf("path = %s, want %s (reason %q)", b.Path(), PathSkip, reason)
	}
	if !strings.Contains(reason, "--rebuild") {
		t.Errorf("reason %q does not say how to build again", reason)
	}
	res, err := b.Build(context.Background(), Request{Image: "reg.io/etl:v1-abc", Platforms: []string{"linux/amd64"}, Push: true, Pin: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Ref != "reg.io/etl@sha256:abc" || !res.Pushed || res.Path != PathSkip {
		t.Errorf("result = %+v, want the registry's pin, pushed, path skip", res)
	}
	if len(reg.asked) != 1 || reg.asked[0] != "reg.io/etl:v1-abc" {
		t.Errorf("registry asked %v, want the image once", reg.asked)
	}
}

// A single-platform image (no index) is reused only for its own platform.
func TestSelectReadsThePlatformOfASingleImage(t *testing.T) {
	for _, tc := range []struct {
		have, want string
		reuse      bool
	}{
		{"linux/amd64", "linux/amd64", true},
		{"linux/arm64/v8", "linux/arm64", true},
		{"linux/arm64", "linux/amd64", false},
		{"linux/arm/v6", "linux/arm/v7", false},
		{"linux/arm", "linux/arm/v7", false},
		{"linux/arm/v7", "linux/arm/v7", true},
		{"linux/arm/v7", "linux/arm", true},
	} {
		reg := &fixedRegistry{pinned: Pinned{Ref: "r@sha256:1", Manifest: []byte(`{"layers":[]}`)}, platform: tc.have}
		b, _ := Select(context.Background(), Options{Reuse: true, Registry: reg}, "r:1", []string{tc.want})
		if got := b.Path() == PathSkip; got != tc.reuse {
			t.Errorf("image for %s, build for %s: reused = %v, want %v", tc.have, tc.want, got, tc.reuse)
		}
	}
}

// What the existing image cannot serve is built: a missing platform, a tag
// the registry does not have, no platform asked for (the tool would pick
// its host's), or reuse not asked for (a tag that does not name content, or
// --rebuild).
func TestSelectBuildsWhenTheImageCannotBeReused(t *testing.T) {
	ok := &fixedRegistry{pinned: Pinned{Ref: "r@sha256:1", Manifest: []byte(amd64Index)}}
	cases := map[string]struct {
		o         Options
		platforms []string
		why       string
	}{
		"missing platform": {Options{Reuse: true, Registry: ok}, []string{"linux/amd64", "linux/arm64"}, "linux/arm64"},
		"not in registry":  {Options{Reuse: true, Registry: &fixedRegistry{err: fmt.Errorf("reading: %w", errors.New("MANIFEST_UNKNOWN"))}}, []string{"linux/amd64"}, "no reusable image for this content in the registry (MANIFEST_UNKNOWN)"},
		"unauthorized":     {Options{Reuse: true, Registry: &fixedRegistry{err: fmt.Errorf("reading: %w", errors.New("UNAUTHORIZED"))}}, []string{"linux/amd64"}, "(UNAUTHORIZED)"},
		"no platform":      {Options{Reuse: true, Registry: ok}, nil, "fallback"},
		"reuse not asked":  {Options{Registry: ok}, []string{"linux/amd64"}, "fallback"},
	}
	for name, tc := range cases {
		b, reason := Select(context.Background(), tc.o, "r:1", tc.platforms)
		if b.Path() != PathLocalDaemon {
			t.Errorf("%s: path = %s, want %s", name, b.Path(), PathLocalDaemon)
		}
		if !strings.Contains(reason, tc.why) {
			t.Errorf("%s: reason %q does not say %q", name, reason, tc.why)
		}
	}
	if len(ok.asked) != 1 {
		t.Errorf("the registry was asked %d times, want once (only the missing-platform case asks)", len(ok.asked))
	}
}
