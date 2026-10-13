package cli

import (
	"slices"
	"strings"
	"testing"
)

// The registry already holds an image under the content label (another
// target, a CI job, a deploy whose register failed): deploy pins it and
// registers it without building or pushing, and says so.
func TestDeployReusesTheImageForItsContentLabel(t *testing.T) {
	dir, reg := platformProjectWith(t, amd64AndArm64, "build:\n  platforms: [linux/amd64, linux/arm64]\n")
	builder, log := argvBuilder(t)
	fake := &deployServer{}
	out, _, err := deployDir(t, fake, dir, builder)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if calls := argvLines(t, log); calls != nil {
		t.Errorf("the builder ran: %q", calls)
	}
	if !strings.Contains(out, "builder:    skip, the registry already holds the image for this content label") {
		t.Errorf("output does not name the skip path:\n%s", out)
	}
	if fake.registers != 1 || !strings.Contains(out, reg.digest) {
		t.Errorf("registers=%d, want the registry's digest %s registered:\n%s", fake.registers, reg.digest, out)
	}
	if want := reg.host + "/team/etl:" + labelOf(t, dir); !slices.Contains(reg.readList(), strings.TrimPrefix(want, reg.host+"/")) {
		t.Errorf("registry reads %v, want the content-label tag %s", reg.readList(), want)
	}
}

// --rebuild, --force and an explicit --dag-version build even when the
// registry holds an image under the tag.
func TestDeployBuildsWhenReuseIsNotAllowed(t *testing.T) {
	for _, args := range [][]string{{"--rebuild"}, {"--force"}, {"--dag-version", "v9"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir, _ := platformProjectWith(t, amd64AndArm64, "build:\n  platforms: [linux/amd64]\n")
			builder, log := argvBuilder(t)
			out, _, err := deployDir(t, &deployServer{}, dir, builder, args...)
			if err != nil {
				t.Fatalf("deploy %v: %v\n%s", args, err, out)
			}
			calls := argvLines(t, log)
			if len(calls) != 2 || !strings.HasPrefix(calls[0], "build ") || !strings.HasPrefix(calls[1], "push ") {
				t.Errorf("builder calls = %q, want a build and a push", calls)
			}
		})
	}
}

func TestReusableImageNeedsTheDefaultContentLabelAsTag(t *testing.T) {
	cases := []struct {
		image, version string
		origin         labelOrigin
		want           bool
	}{
		{"reg.io/etl:v1-abc", "v1-abc", labelDefault, true},
		{"reg.io/etl:v1-abc", "v1-abc", labelExplicit, false},
		{"reg.io/etl:1a2b3c4", "v1-abc", labelDefault, false},
		{"reg.io/etl:x-v1-abc", "v1-abc", labelDefault, false},
		{"reg.io/etl:", "", labelDefault, false},
	}
	for _, tc := range cases {
		if got := reusableImage(tc.image, tc.version, tc.origin); got != tc.want {
			t.Errorf("reusableImage(%q, %q, %v) = %v, want %v", tc.image, tc.version, tc.origin, got, tc.want)
		}
	}
}

// --plan reads the registry as the deploy would: an image already there for
// the content label is planned as a reuse, not a build.
func TestDeployPlanSaysTheImageIsReused(t *testing.T) {
	dir, _ := platformProjectWith(t, amd64AndArm64, "build:\n  platforms: [linux/amd64]\n")
	builder, log := argvBuilder(t)
	out, _, err := deployDir(t, &deployServer{status: 404}, dir, builder, "--plan")
	if err != nil {
		t.Fatalf("plan: %v\n%s", err, out)
	}
	for _, want := range []string{"build       no", "push        no", "the registry already holds the image for this content label"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan does not say %q:\n%s", want, out)
		}
	}
	if argvLines(t, log) != nil {
		t.Error("--plan ran the builder")
	}
	out, _, err = deployDir(t, &deployServer{status: 404}, dir, builder, "--plan", "--rebuild")
	if err != nil || !strings.Contains(out, "build       yes") {
		t.Errorf("plan --rebuild: %v, want a build:\n%s", err, out)
	}
}
