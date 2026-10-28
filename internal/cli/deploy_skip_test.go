package cli

import (
	"os"
	"path/filepath"
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
	// This deploy built nothing: the image came from an earlier build this
	// CLI knows nothing of, so the register names no builder and no base
	// image (#1702), and still sends what it does know.
	if len(fake.regBuilds) != 1 {
		t.Fatalf("registers = %d, want 1", len(fake.regBuilds))
	}
	if b := fake.regBuilds[0]; b.Has("build_builder") || b.Has("build_base_image") || !b.Has("build_cli_version") {
		t.Errorf("build info of a reused image = %v, want no builder or base image, and the CLI version", b)
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
	t.Parallel()
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

// tag_strategy git_sha in a git repository tags with the commit, which names
// no content (the tree may be dirty): the deploy builds even though the
// registry answers for that tag.
func TestDeployWithAGitTagBuilds(t *testing.T) {
	dir, _ := platformProjectWith(t, amd64AndArm64, "build:\n  platforms: [linux/amd64]\n")
	writeProjectFile(t, dir, "dexaflow.yaml", strings.Replace(readProjectFile(t, dir, "dexaflow.yaml"),
		"  image_name: etl\n", "  image_name: etl\n  tag_strategy: git_sha\n", 1))
	gitInitTagged(t, dir, "")
	builder, log := argvBuilder(t)
	out, _, err := deployDir(t, &deployServer{}, dir, builder)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if calls := argvLines(t, log); len(calls) != 2 || !strings.HasPrefix(calls[0], "build ") {
		t.Errorf("builder calls = %q, want a build and a push", calls)
	}
}

// The checks before a build hold on the skip path: a build.dockerfile that a
// symlink takes outside the project is refused (#1272) even when the image
// is reused.
func TestDeployReuseKeepsTheDockerfileCheck(t *testing.T) {
	dir, _ := platformProjectWith(t, amd64AndArm64, "build:\n  platforms: [linux/amd64]\n  dockerfile: Dockerfile.link\n")
	outside := filepath.Join(t.TempDir(), "Dockerfile")
	writeProjectFile(t, filepath.Dir(outside), "Dockerfile", "FROM scratch\n")
	if err := os.Symlink(outside, filepath.Join(dir, "Dockerfile.link")); err != nil {
		t.Fatal(err)
	}
	builder, _ := argvBuilder(t)
	fake := &deployServer{}
	out, _, err := deployDir(t, fake, dir, builder)
	if err == nil || !strings.Contains(err.Error(), "outside the project directory") || fake.registers != 0 {
		t.Fatalf("deploy with build.dockerfile outside the project: err=%v registers=%d\n%s", err, fake.registers, out)
	}
}

// A plan that builds nothing (the label is current) never claims a reuse.
func TestDeployPlanOfTheCurrentLabelClaimsNoReuse(t *testing.T) {
	dir, _ := platformProjectWith(t, amd64AndArm64, "build:\n  platforms: [linux/amd64]\n")
	label := labelOf(t, dir)
	builder, _ := argvBuilder(t)
	out, _, err := deployDir(t, &deployServer{current: label, registered: []string{label}}, dir, builder, "--plan")
	if err != nil || !strings.Contains(out, "up to date") || strings.Contains(out, "registry already holds") {
		t.Errorf("plan of the current label: %v\n%s", err, out)
	}
}

func readProjectFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel)) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
