package config

import (
	"strings"
	"testing"
)

// testRuntimeImage is a runtime image pinned by a well-formed sha256 digest.
var testRuntimeImage = "ghcr.io/dexadata/runtime:0.5.3@sha256:" + strings.Repeat("0123456789abcdef", 4)

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
	t.Setenv("DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE", testRuntimeImage)
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	want := SourceModeSection{Enabled: true, Image: testRuntimeImage}
	if c.Execution.SourceMode != want {
		t.Errorf("execution.source_mode = %+v, want %+v from env", c.Execution.SourceMode, want)
	}
}

// The legacy LEOFLOW_ names still bind (ADR 0067 §3), and the DEXAFLOW_ name
// wins when both are set.
func TestSourceModeLegacyEnvBinds(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTION_SOURCE_MODE_ENABLED", "true")
	t.Setenv("LEOFLOW_EXECUTION_SOURCE_MODE_IMAGE", testRuntimeImage)
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	want := SourceModeSection{Enabled: true, Image: testRuntimeImage}
	if c.Execution.SourceMode != want {
		t.Errorf("execution.source_mode = %+v, want %+v from LEOFLOW_ env", c.Execution.SourceMode, want)
	}

	t.Setenv("DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE", testRuntimeImage+"x")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Execution.SourceMode.Image != testRuntimeImage+"x" {
		t.Errorf("image = %q, want the DEXAFLOW_ value to win over LEOFLOW_", c.Execution.SourceMode.Image)
	}
}

func TestSourceModeRuntimeImage(t *testing.T) {
	if got := (SourceModeSection{Enabled: true, Image: testRuntimeImage}).RuntimeImage(); got != testRuntimeImage {
		t.Errorf("RuntimeImage() on = %q, want the image", got)
	}
	if got := (SourceModeSection{Enabled: false, Image: testRuntimeImage}).RuntimeImage(); got != "" {
		t.Errorf("RuntimeImage() off = %q, want empty", got)
	}
}

func TestValidateSourceMode(t *testing.T) {
	cases := map[string]struct {
		mode    SourceModeSection
		wantErr string
	}{
		"off":                     {SourceModeSection{}, ""},
		"off with any image":      {SourceModeSection{Image: "rt:latest"}, ""},
		"on with a digest":        {SourceModeSection{Enabled: true, Image: testRuntimeImage}, ""},
		"on without image":        {SourceModeSection{Enabled: true}, "execution.source_mode.image is required"},
		"on with a tag only":      {SourceModeSection{Enabled: true, Image: "ghcr.io/dexadata/runtime:0.5.3"}, "pinned by digest"},
		"on with a short digest":  {SourceModeSection{Enabled: true, Image: "ghcr.io/dexadata/runtime@sha256:abc"}, "pinned by digest"},
		"on with an empty digest": {SourceModeSection{Enabled: true, Image: "ghcr.io/dexadata/runtime@sha256:"}, "pinned by digest"},
		"on with a trailing tag":  {SourceModeSection{Enabled: true, Image: testRuntimeImage + ":latest"}, "pinned by digest"},
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
