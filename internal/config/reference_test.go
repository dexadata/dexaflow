package config

import (
	"testing"
)

// TestServerSettingsCoverDefaults locks the two directions the reference page
// relies on: every key in serverDefaults is a field of ServerConfig (a default
// with no field is a variable the page would document and the server ignore),
// and every field without a default is listed as file-only.
func TestServerSettingsCoverDefaults(t *testing.T) {
	settings := ServerSettings()
	byKey := map[string]Setting{}
	for _, s := range settings {
		if _, dup := byKey[s.Key]; dup {
			t.Errorf("key %q listed twice", s.Key)
		}
		byKey[s.Key] = s
	}
	for key := range serverDefaults {
		s, ok := byKey[key]
		if !ok {
			t.Errorf("serverDefaults has %q, which is no field of ServerConfig", key)
			continue
		}
		if s.Env == "" {
			t.Errorf("%q has a default but no environment variable", key)
		}
	}
	for _, s := range settings {
		if _, ok := serverDefaults[s.Key]; !ok && s.Env != "" {
			t.Errorf("%q has no default but names variable %s", s.Key, s.Env)
		}
	}
}

// TestServerSettingsShape checks the derived names, types and defaults on keys
// whose binding the loader tests already prove.
func TestServerSettingsShape(t *testing.T) {
	want := map[string]Setting{
		"auth.oidc.client_id": {Env: "DEXAFLOW_AUTH_OIDC_CLIENT_ID", LegacyEnv: "LEOFLOW_AUTH_OIDC_CLIENT_ID", Type: "string", Owner: "OIDCSection", Field: "ClientID"},
		"auth.oidc.scopes":    {Env: "DEXAFLOW_AUTH_OIDC_SCOPES", LegacyEnv: "LEOFLOW_AUTH_OIDC_SCOPES", Type: "list", Default: "openid,email,profile", Owner: "OIDCSection", Field: "Scopes"},
		// The dotted-key maps are read from the file only (#826).
		"auth.oidc.tenant_claims":         {Type: "map", Owner: "OIDCSection", Field: "TenantClaims"},
		"server.read_timeout":             {Env: "DEXAFLOW_SERVER_READ_TIMEOUT", LegacyEnv: "LEOFLOW_SERVER_READ_TIMEOUT", Type: "duration", Default: "0s", Owner: "ServerSection", Field: "ReadTimeout"},
		"observability.otel.sample_ratio": {Env: "DEXAFLOW_OBSERVABILITY_OTEL_SAMPLE_RATIO", LegacyEnv: "LEOFLOW_OBSERVABILITY_OTEL_SAMPLE_RATIO", Type: "float", Default: "1", Owner: "OTelSection", Field: "SampleRatio"},
		"secret_key":                      {Env: "DEXAFLOW_SECRET_KEY", LegacyEnv: "LEOFLOW_SECRET_KEY", Type: "string", Owner: "ServerConfig", Field: "SecretKey"},
		// Fields of a struct embedded with mapstructure ",squash" sit at the
		// embedding struct's level.
		"deploy.defaults.platforms": {Env: "DEXAFLOW_DEPLOY_DEFAULTS_PLATFORMS", LegacyEnv: "LEOFLOW_DEPLOY_DEFAULTS_PLATFORMS", Type: "list", Owner: "DeployDefaults", Field: "Platforms"},
	}
	got := map[string]Setting{}
	for _, s := range ServerSettings() {
		got[s.Key] = s
	}
	for key, w := range want {
		w.Key = key
		if got[key] != w {
			t.Errorf("%s:\n got  %+v\n want %+v", key, got[key], w)
		}
	}
}

// TestCLISettings checks that only the keys Load registers name a variable:
// the Lite keys written by `dexaflow setup` are file-only.
func TestCLISettings(t *testing.T) {
	got := map[string]Setting{}
	for _, s := range CLISettings() {
		got[s.Key] = s
	}
	if s := got["token"]; s.Env != "DEXAFLOW_TOKEN" || s.LegacyEnv != "LEOFLOW_TOKEN" {
		t.Errorf("token = %+v, want DEXAFLOW_TOKEN / LEOFLOW_TOKEN", s)
	}
	if s := got["server_url"]; s.Default != defaultServerURL {
		t.Errorf("server_url default = %q, want %q", s.Default, defaultServerURL)
	}
	if s := got["workspace"]; s.Key == "" || s.Env != "" {
		t.Errorf("workspace = %+v, want a file-only key", s)
	}
}
