package main

import "errors"

// guardInsecureSecretsForEdition refuses the dev-only insecure-secrets escape
// hatch when the chart marks the deployment as the Pro edition (#58, Pro alpha
// blocker). The flag is a deliberate dev convenience for Lite that lets the
// control plane ship secrets over a plaintext gRPC channel — fine on
// localhost, never inside a real cluster.
//
// The Helm chart sets LEOFLOW_UI_EDITION=pro on every Pro install; Lite
// (edition=lite) and the unmarked default (edition="") still allow the flag so
// the inner dev loop keeps working without TLS.
func guardInsecureSecretsForEdition(edition string, allowInsecure bool) error {
	if !allowInsecure {
		return nil
	}
	if edition != "pro" {
		return nil
	}
	return errors.New("LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS=true is not allowed in the Pro edition: " +
		"a Pro deployment ships secrets only over the TLS-enabled gRPC channel — " +
		"either set agentTLS.enabled=true in the Helm values (with cert-manager) or unset the env var " +
		"(see ADR 0014 and issue #58)")
}

// guardEditionSecurity runs the edition-gated boot guards in order: the
// insecure-secrets escape hatch must not be set in Pro, and Pro requires TLS on
// the agent gRPC channel. It returns the first failure so run() keeps a single
// boot gate for edition security.
func guardEditionSecurity(edition string, allowInsecure bool, grpcTLSCert, grpcTLSKey string) error {
	if err := guardInsecureSecretsForEdition(edition, allowInsecure); err != nil {
		return err
	}
	return guardTLSForEdition(edition, grpcTLSCert, grpcTLSKey)
}

// guardTLSForEdition refuses boot when a Pro deployment has no TLS on the agent
// gRPC channel (#281). Without it the control plane boots and looks healthy, but
// guardSecretChannel then rejects every secrets RPC to a task pod
// ("refusing to send secrets over an insecure channel") — tasks queue/fail with
// no obvious cause. An operator who overrides agentTLS.enabled=false hits exactly
// this. Fail loudly at boot instead. Lite (edition=lite) and the unmarked default
// keep the plaintext dev loop.
func guardTLSForEdition(edition, grpcTLSCert, grpcTLSKey string) error {
	if edition != "pro" {
		return nil
	}
	if grpcTLSCert != "" && grpcTLSKey != "" {
		return nil
	}
	return errors.New("the Pro edition requires TLS on the agent gRPC channel, but it is off: " +
		"set both LEOFLOW_SERVER_GRPC_TLS_CERT and LEOFLOW_SERVER_GRPC_TLS_KEY (the Helm chart " +
		"sets them from agentTLS.enabled=true + cert-manager). Booting without them would look " +
		"healthy but every secrets RPC to a task pod would fail (PermissionDenied: refusing to " +
		"send secrets over an insecure channel), leaving tasks stuck. See ADR 0014, #58, #281")
}
