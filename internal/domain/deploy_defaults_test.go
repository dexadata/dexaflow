package domain

import (
	"strings"
	"testing"
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
