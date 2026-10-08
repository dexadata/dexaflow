package config

import (
	"strings"
	"testing"
)

// execution.source_mode (ADR 0067 §3): off by default; on, it needs a runtime
// image pinned by digest.

func TestSourceModeDefaultsOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Execution.SourceMode != (SourceModeSection{}) {
		t.Errorf("execution.source_mode default = %+v, want off with no image", c.Execution.SourceMode)
	}
	if got := c.Execution.SourceMode.RuntimeImage(); got != "" {
		t.Errorf("RuntimeImage() = %q, want empty when off", got)
	}
}

func TestSourceModeEnvBinds(t *testing.T) {
	t.Setenv("DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED", "true")
	t.Setenv("DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE", "rt@sha256:abc")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	want := SourceModeSection{Enabled: true, Image: "rt@sha256:abc"}
	if c.Execution.SourceMode != want {
		t.Errorf("execution.source_mode = %+v, want %+v from env", c.Execution.SourceMode, want)
	}
}

func TestSourceModeRuntimeImage(t *testing.T) {
	if got := (SourceModeSection{Enabled: true, Image: "rt@sha256:abc"}).RuntimeImage(); got != "rt@sha256:abc" {
		t.Errorf("RuntimeImage() on = %q, want the image", got)
	}
	if got := (SourceModeSection{Enabled: false, Image: "rt@sha256:abc"}).RuntimeImage(); got != "" {
		t.Errorf("RuntimeImage() off = %q, want empty", got)
	}
}

func TestValidateSourceMode(t *testing.T) {
	cases := map[string]struct {
		mode    SourceModeSection
		wantErr string
	}{
		"off":                {SourceModeSection{}, ""},
		"off with any image": {SourceModeSection{Image: "rt:latest"}, ""},
		"on with a digest":   {SourceModeSection{Enabled: true, Image: "ghcr.io/dexadata/runtime:0.5.3@sha256:abc"}, ""},
		"on without image":   {SourceModeSection{Enabled: true}, "execution.source_mode.image"},
		"on with a tag only": {SourceModeSection{Enabled: true, Image: "ghcr.io/dexadata/runtime:0.5.3"}, "pinned by digest"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := validWarmConfig()
			c.Execution.SourceMode = tc.mode
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Validate() = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}
