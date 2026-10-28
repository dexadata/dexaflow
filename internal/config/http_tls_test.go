package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHTTPTLSDefaultIsPlainHTTP locks the behavior neutral default (#638): with
// neither key set the API listener serves plain HTTP, exactly as before.
func TestHTTPTLSDefaultIsPlainHTTP(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Server.HTTPTLSCert != "" || c.Server.HTTPTLSKey != "" {
		t.Errorf("server.http_tls_cert/key default = %q/%q, want both empty", c.Server.HTTPTLSCert, c.Server.HTTPTLSKey)
	}
	if c.Server.HTTPTLSEnabled() {
		t.Error("HTTPTLSEnabled() = true on a default config, want false (plain HTTP stays the default)")
	}
}

// TestHTTPTLSBindsBothEnvPrefixes proves both keys bind from the current
// DEXAFLOW_* names and from the pre-rename LEOFLOW_* names.
func TestHTTPTLSBindsBothEnvPrefixes(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"SERVER_HTTP_TLS_CERT", "/certs/tls.crt")
			t.Setenv(prefix+"SERVER_HTTP_TLS_KEY", "/certs/tls.key")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if c.Server.HTTPTLSCert != "/certs/tls.crt" || c.Server.HTTPTLSKey != "/certs/tls.key" {
				t.Errorf("server.http_tls_cert/key = %q/%q, want /certs/tls.crt and /certs/tls.key from %s*",
					c.Server.HTTPTLSCert, c.Server.HTTPTLSKey, prefix)
			}
			if !c.Server.HTTPTLSEnabled() {
				t.Error("HTTPTLSEnabled() = false with both keys set, want true")
			}
		})
	}
}

// TestHTTPTLSBindsFromConfigFile proves the keys bind from the config file,
// whichever name it has.
func TestHTTPTLSBindsFromConfigFile(t *testing.T) {
	for _, name := range []string{"dexaflow.yaml", "leoflow.yaml"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), name)
			body := "server:\n  http_tls_cert: /etc/tls/tls.crt\n  http_tls_key: /etc/tls/tls.key\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadServer(path, nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if c.Server.HTTPTLSCert != "/etc/tls/tls.crt" || c.Server.HTTPTLSKey != "/etc/tls/tls.key" {
				t.Errorf("server.http_tls_cert/key = %q/%q, want the config file values", c.Server.HTTPTLSCert, c.Server.HTTPTLSKey)
			}
		})
	}
}

// TestValidateHTTPTLSRequiresBothKeys fails closed when only one half of the
// pair is set: a cert without its key (or the reverse) is a misconfiguration
// that would otherwise silently keep serving plain HTTP.
func TestValidateHTTPTLSRequiresBothKeys(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cert, key string
		wantErr   bool
	}{
		{"neither", "", "", false},
		{"both", "/c", "/k", false},
		{"cert only", "/c", "", true},
		{"key only", "", "/k", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Auth.JWT.Secret = "set"
			c.Server.HTTPTLSCert = tc.cert
			c.Server.HTTPTLSKey = tc.key
			err := c.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatal("Validate() = nil, want an error naming the missing half")
				}
				if !strings.Contains(err.Error(), "http_tls_cert") || !strings.Contains(err.Error(), "http_tls_key") {
					t.Errorf("Validate() error = %q, want it to name server.http_tls_cert and server.http_tls_key", err)
				}
				return
			}
			if err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}
