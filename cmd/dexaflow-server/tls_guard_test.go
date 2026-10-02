package main

import (
	"strings"
	"testing"
)

// TestRefuseInsecureSecretsInProd: a Pro chart deployment (edition=pro)
// MUST NOT boot with LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true. The dev-only
// escape hatch is fine on Lite, but inside a real cluster shipping secrets over
// a plaintext gRPC channel is the kind of mistake that should be loud (#58).
//
// Lite (edition=lite) and the default (edition="") still tolerate the flag so
// the local dev loop keeps working.
func TestRefuseInsecureSecretsInProd(t *testing.T) {
	cases := []struct {
		name             string
		edition          string
		allowInsecure    bool
		wantErrSubstring string // empty = expect no error
	}{
		{name: "lite + insecure: allowed (dev/inner loop)", edition: "lite", allowInsecure: true},
		{name: "empty edition + insecure: allowed (dev fallback)", edition: "", allowInsecure: true},
		{name: "pro + insecure: REFUSED", edition: "pro", allowInsecure: true,
			wantErrSubstring: "LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS"},
		{name: "pro + NO insecure: allowed (default secure)", edition: "pro", allowInsecure: false},
		{name: "lite + NO insecure: allowed", edition: "lite", allowInsecure: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guardInsecureSecretsForEdition(tc.edition, tc.allowInsecure)
			if tc.wantErrSubstring == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstring)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErrSubstring)
			}
		})
	}
}

// TestRefuseProWithoutTLS: the Pro edition requires TLS on the agent gRPC channel.
// An operator who overrides agentTLS.enabled=false boots a control plane that looks
// healthy, then every secrets RPC to a task pod fails cryptically. Refuse it loudly
// at boot instead (#281). Lite + the unmarked default keep the plaintext dev loop.
func TestRefuseProWithoutTLS(t *testing.T) {
	cases := []struct {
		name             string
		edition          string
		cert, key        string
		wantErrSubstring string // empty = expect no error
	}{
		{name: "pro + cert+key: OK", edition: "pro", cert: "/c.pem", key: "/k.pem"},
		{name: "pro + no cert: REFUSED", edition: "pro", cert: "", key: "/k.pem",
			wantErrSubstring: "LEOFLOW_SERVER_GRPC_TLS_CERT"},
		{name: "pro + no key: REFUSED", edition: "pro", cert: "/c.pem", key: "",
			wantErrSubstring: "LEOFLOW_SERVER_GRPC_TLS_KEY"},
		{name: "pro + neither: names the chart value", edition: "pro", cert: "", key: "",
			wantErrSubstring: "agentTLS.enabled"},
		{name: "lite + no TLS: allowed (dev)", edition: "lite", cert: "", key: ""},
		{name: "empty edition + no TLS: allowed (dev fallback)", edition: "", cert: "", key: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guardTLSForEdition(tc.edition, tc.cert, tc.key)
			if tc.wantErrSubstring == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErrSubstring)
			}
			if !strings.Contains(err.Error(), tc.wantErrSubstring) {
				t.Errorf("error %q missing %q", err.Error(), tc.wantErrSubstring)
			}
		})
	}
}
