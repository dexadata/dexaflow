package cli

import (
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// `leoflow validate` checked dag.py's syntax under whatever interpreter it
// found, never consulting python_version (#1094). The failure is the annoying
// direction: valid 3.12+ code is REJECTED, because `type X[T]` is a SyntaxError
// on 3.11, so validate fails a DAG the cluster runs fine and the author has no
// way to tell the complaint is about the checker.
//
// It now asks the same question `leoflow dev` asks, with the same exemptions.
func TestValidateEnforcesTheDeclaredPythonVersion(t *testing.T) {
	cases := []struct {
		name string
		cfg  *domain.LeoflowConfig
		want string
	}{
		{
			name: "a declared version is honored",
			cfg:  &domain.LeoflowConfig{PythonVersion: "3.13"},
			want: "3.13",
		},
		{
			name: "a defaulted version is not: the author said nothing, so any supported interpreter is fair",
			cfg:  &domain.LeoflowConfig{PythonVersion: "3.11", PythonVersionDefaulted: true},
			want: "",
		},
		{
			name: "base_image wins: python_version is inert when the FROM is chosen by hand",
			cfg:  &domain.LeoflowConfig{PythonVersion: "3.13", BaseImage: "ghcr.io/x/y:tag"},
			want: "",
		},
		{
			name: "no config at all",
			cfg:  nil,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateEnforcedPythonVersion(tc.cfg); got != tc.want {
				t.Errorf("validateEnforcedPythonVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

// A deprecated version must not be enforced: the project is on its way off it,
// and refusing to lint until the author installs an interpreter we are telling
// them to abandon helps nobody. Same exemption `leoflow dev` makes.
func TestValidateDoesNotEnforceADeprecatedVersion(t *testing.T) {
	// 3.10 is the version the schema marks deprecated (see .changie/the schema's
	// x-leoflow-python-deprecations); skip if that ever stops being true rather
	// than asserting against a moving list.
	const dep = "3.10"
	if _, ok := domain.DeprecatedPythonVersion(dep); !ok {
		t.Skipf("%s is no longer marked deprecated", dep)
	}
	if got := validateEnforcedPythonVersion(&domain.LeoflowConfig{PythonVersion: dep}); got != "" {
		t.Errorf("a deprecated version (%s) must not be enforced, got %q", dep, got)
	}
}
