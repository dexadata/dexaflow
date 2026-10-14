package domain

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
)

func TestDeployDefaultsValidateAcceptsUsableValues(t *testing.T) {
	cases := []DeployDefaults{
		{},
		{Registry: "ghcr.io/acme"},
		{Registry: "localhost:5000"},
		{Registry: "localhost:5000/team/dags"},
		{Registry: "123456789012.dkr.ecr.us-east-1.amazonaws.com/dags"},
		{Platforms: []string{"linux/amd64", "linux/arm64/v8"}},
		{BaseImage: "ghcr.io/acme/base:1"},
		{BaseImage: "python:3.12-slim@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	for _, d := range cases {
		if err := d.Validate("deploy.defaults"); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", d, err)
		}
	}
}

func TestDeployDefaultsValidateRefusesWhatABuildCannotUse(t *testing.T) {
	cases := []struct {
		name  string
		d     DeployDefaults
		field string
	}{
		{"scheme", DeployDefaults{Registry: "https://ghcr.io/acme"}, "deploy.defaults.registry"},
		{"credentials", DeployDefaults{Registry: "bob:secret@ghcr.io/acme"}, "deploy.defaults.registry"},
		{"tag", DeployDefaults{Registry: "ghcr.io/acme:1"}, "deploy.defaults.registry"},
		{"uppercase", DeployDefaults{Registry: "ghcr.io/Acme"}, "deploy.defaults.registry"},
		{"trailing slash", DeployDefaults{Registry: "ghcr.io/acme/"}, "deploy.defaults.registry"},
		{"whitespace", DeployDefaults{Registry: "ghcr.io/ac me"}, "deploy.defaults.registry"},
		{"platform form", DeployDefaults{Platforms: []string{"amd64"}}, "deploy.defaults.platforms"},
		{"empty platform", DeployDefaults{Platforms: []string{""}}, "deploy.defaults.platforms"},
		{"base image newline", DeployDefaults{BaseImage: "python:3.12\nRUN id"}, "deploy.defaults.base_image"},
		{"base image lexer", DeployDefaults{BaseImage: "python:$TAG"}, "deploy.defaults.base_image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.d.Validate("deploy.defaults")
			if err == nil {
				t.Fatalf("Validate(%+v) = nil, want an error", tc.d)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %s", err, tc.field)
			}
		})
	}
}

func TestDeployDefaultsIsEmpty(t *testing.T) {
	if !(DeployDefaults{}).IsEmpty() {
		t.Error("zero value is not empty")
	}
	for _, d := range []DeployDefaults{{Registry: "r"}, {Platforms: []string{"linux/amd64"}}, {BaseImage: "b"}} {
		if d.IsEmpty() {
			t.Errorf("%+v reported empty", d)
		}
	}
}

// TestPlatformPatternMatchesTheBuilder: the server refuses at boot exactly the
// platforms a build would refuse, so the two expressions stay equal.
func TestPlatformPatternMatchesTheBuilder(t *testing.T) {
	if got, want := platformPattern.String(), build.PlatformPattern; got != want {
		t.Errorf("domain platformPattern = %q, build.PlatformPattern = %q; keep them equal", got, want)
	}
}

// pinnedRuntime is a runtime image pinned by a full digest, the only form
// source mode accepts (ADR 0067).
const pinnedRuntime = "ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestDeployDefaultsSourceModeImage (#1772): the runtime image a cluster
// advertises for source mode counts as a default, and only a full digest
// pin is usable, as the server's own execution.source_mode.image.
func TestDeployDefaultsSourceModeImage(t *testing.T) {
	d := DeployDefaults{SourceModeImage: pinnedRuntime}
	if d.IsEmpty() {
		t.Error("defaults with only a source mode image reported empty")
	}
	if err := d.Validate("deploy.defaults"); err != nil {
		t.Errorf("Validate(pinned) = %v, want nil", err)
	}
	for _, bad := range []string{"ghcr.io/dexadata/dexaflow-runtime:0.5.3", "ghcr.io/dexadata/dexaflow-runtime@sha256:0123"} {
		err := DeployDefaults{SourceModeImage: bad}.Validate("deploy.defaults")
		if err == nil || !strings.Contains(err.Error(), "deploy.defaults.source_mode_image") {
			t.Errorf("Validate(%q) = %v, want an error naming deploy.defaults.source_mode_image", bad, err)
		}
	}
}
