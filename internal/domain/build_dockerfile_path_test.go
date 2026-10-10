package domain

import (
	"errors"
	"strings"
	"testing"
)

func buildDockerfileCfg(dockerfile string) *LeoflowConfig {
	c := &LeoflowConfig{SchemaVersion: "1.0", DagID: "sales", Build: &BuildConfig{Dockerfile: dockerfile}}
	c.ApplyDefaults()
	return c
}

// build.dockerfile was returned verbatim and joined onto the project directory,
// so `../../other/Dockerfile` built from a file outside the project and skipped
// every check the generated Dockerfile gets (#1272). include_paths, dbt.project
// and dbt_groups already refuse those shapes; this is the same rule, a test per
// shape like theirs.
func TestValidateRejectsABuildDockerfileOutsideTheProject(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		wants string
	}{
		{"absolute", "/srv/other/Dockerfile", "absolute"},
		{"absolute root", "/", "absolute"},
		{"escapes upward", "../../other/Dockerfile", "outside"},
		{"escapes one level", "../Dockerfile", "outside"},
		{"escapes after descending", "docker/../../Dockerfile", "outside"},
		{"parent itself", "..", "outside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := buildDockerfileCfg(tc.value).Validate()
			if err == nil {
				t.Fatalf("build.dockerfile %q was accepted", tc.value)
			}
			if !errors.Is(err, ErrInvalidBuildDockerfile) {
				t.Errorf("want ErrInvalidBuildDockerfile, got: %v", err)
			}
			for _, want := range []string{"build.dockerfile", tc.value, tc.wants} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error must mention %q, got: %v", want, err)
				}
			}
		})
	}
}

// Every relative path that stays inside the project keeps working, including
// one spelled with a `..` that comes back in.
func TestValidateAcceptsABuildDockerfileInsideTheProject(t *testing.T) {
	for _, ok := range []string{
		"",
		"Dockerfile",
		"./Dockerfile",
		"docker/Dockerfile.prod",
		"docker/../Dockerfile",
		"..Dockerfile",
	} {
		if err := buildDockerfileCfg(ok).Validate(); err != nil {
			t.Errorf("build.dockerfile %q refused: %v", ok, err)
		}
	}
}
