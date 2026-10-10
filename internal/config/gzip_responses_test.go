package config

import "testing"

// TestGzipResponsesDefaultsOff pins the gate's default (ADR 0062).
func TestGzipResponsesDefaultsOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Server.GzipResponses {
		t.Fatal("server.gzip_responses defaults to true, want false")
	}
}

// TestGzipResponsesBindsBothPrefixes pins the env-only Helm path under the
// current and the legacy prefix.
func TestGzipResponsesBindsBothPrefixes(t *testing.T) {
	for _, env := range []string{"DEXAFLOW_SERVER_GZIP_RESPONSES", "LEOFLOW_SERVER_GZIP_RESPONSES"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "true")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			if !c.Server.GzipResponses {
				t.Fatalf("%s=true did not enable server.gzip_responses", env)
			}
		})
	}
}
