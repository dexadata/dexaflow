package cli

import (
	"strings"
	"testing"
)

func TestReleaseBaseTag(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"v0.4.2":                  "v0.4.2",      // clean release tag → pin
		"v0.4.3-rc.1":             "v0.4.3-rc.1", // rc tag is still a release
		"v1.2.3-beta.4":           "v1.2.3-beta.4",
		"v0.4.2-9-gabc1234":       "", // git describe (commits after tag) → moving
		"v0.4.2-9-gabc1234-dirty": "", // describe + dirty
		"v0.4.2-dirty":            "", // dirty tree
		"dev":                     "", // default source build
		"":                        "", // unset
	}
	for in, want := range cases {
		if got := releaseBaseTag(in); got != want {
			t.Errorf("releaseBaseTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBaseImageRef(t *testing.T) {
	t.Parallel()
	const repo = "ghcr.io/dexadata/dexaflow-runtime"
	cases := map[string]string{
		// GoReleaser stamps the CLI version WITHOUT the leading v; the published base
		// tag ALWAYS has it. Both forms must resolve to the same py<ver>-v<X> tag.
		"0.4.2":      repo + ":py3.11-v0.4.2",
		"v0.4.2":     repo + ":py3.11-v0.4.2",
		"0.4.3-rc.1": repo + ":py3.11-v0.4.3-rc.1",
		// dev/dirty/describe → moving tag (no versioned base is published for them).
		"0.4.2-9-gabc1234": repo + ":py3.11",
		"v0.4.2-dirty":     repo + ":py3.11",
		"dev":              repo + ":py3.11",
		"":                 repo + ":py3.11",
	}
	for ver, want := range cases {
		if got := baseImageRef(repo, "3.11", ver); got != want {
			t.Errorf("baseImageRef(_, 3.11, %q) = %q, want %q", ver, got, want)
		}
	}
}

// TestBaseImageNoteNamesTheBaseAndWhy covers the line a generated Dockerfile's
// build prints (#1255): two people compiling the same project can land on
// different bases, and the build should say which one and why.
func TestBaseImageNoteNamesTheBaseAndWhy(t *testing.T) {
	t.Parallel()
	const repo = "ghcr.io/dexadata/dexaflow-runtime"
	cases := []struct {
		name, baseImage, source, cliVersion, want string
	}{
		{"released CLI pins", "", "", "0.5.13", "note: base " + repo + ":py3.11-v0.5.13 (pinned by this release)"},
		{"dev build moves", "", "", "v0.5.13-4-gabc1234", "note: base " + repo + ":py3.11 (moving line; this is a development build)"},
		{"base_image wins", "registry.example/custom:1.2", "dexaflow.yaml", "0.5.13", "note: base registry.example/custom:1.2 (from base_image in dexaflow.yaml)"},
		{"base_image with no known source", "registry.example/custom:1.2", "", "dev", "note: base registry.example/custom:1.2 (from base_image)"},
	}
	for _, c := range cases {
		got := baseImageNote(c.baseImage, c.source, "3.11", c.cliVersion)
		if got != c.want {
			t.Errorf("%s: baseImageNote() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestCompileBuildSaysWhichBaseItResolved drives compile --build: a generated
// Dockerfile's build names its base, and a project's own Dockerfile, whose FROM
// the CLI did not choose, gets no such line (#1255).
func TestCompileBuildSaysWhichBaseItResolved(t *testing.T) {
	cases := []struct {
		name, yaml    string
		ownDockerfile bool
		want, notWant string
	}{
		{name: "default base", want: "note: base " + publishedBaseRepo + ":py"},
		{name: "base_image", yaml: "base_image: registry.example/custom:1.2\n", want: "note: base registry.example/custom:1.2 (from base_image in "},
		{name: "own Dockerfile", ownDockerfile: true, notWant: "note: base "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := platformProject(t, c.yaml)
			if c.ownDockerfile {
				writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
			}
			builder, _ := argvBuilder(t)
			out, errOut, err := run(t, "compile", dir, "--build", "--builder", builder)
			if err != nil {
				t.Fatalf("compile --build: %v\n%s", err, errOut)
			}
			all := out + errOut
			if c.want != "" && !strings.Contains(all, c.want) {
				t.Errorf("output does not say %q:\n%s", c.want, all)
			}
			if c.notWant != "" && strings.Contains(all, c.notWant) {
				t.Errorf("output says %q for a project's own Dockerfile:\n%s", c.notWant, all)
			}
		})
	}
}
