package main

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestWarmPodSpecFuncExchangeTransport: under auth.agent_token_transport=exchange
// the warm pod carries NO plaintext bootstrap token — it projects a ServiceAccount
// token instead (the whole point of ADR 0058 D2), with the control-plane audience.
func TestWarmPodSpecFuncExchangeTransport(t *testing.T) {
	cfg := &config.ServerConfig{}
	cfg.Auth.AgentTokenTransport = config.AgentTokenTransportExchange
	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)

	spec, err := warmPodSpecFunc(cfg, authn, "cp:9000")(executor.WarmTarget{DagVersionID: "dv-1", Image: "reg/dag:v1"})
	if err != nil {
		t.Fatalf("warmPodSpecFunc: %v", err)
	}
	if spec.BootstrapToken != "" {
		t.Error("under the exchange transport the warm pod must carry NO plaintext bootstrap token")
	}
	if spec.AgentTokenTransport != config.AgentTokenTransportExchange {
		t.Errorf("AgentTokenTransport = %q, want exchange", spec.AgentTokenTransport)
	}
	if spec.AgentTokenAudience != executor.DefaultAgentTokenAudience {
		t.Errorf("AgentTokenAudience = %q, want %q", spec.AgentTokenAudience, executor.DefaultAgentTokenAudience)
	}
}

// TestWarmPodSpecFuncEnvVarFallbackIsWorkerScoped: the env-var fallback mints a
// bootstrap token that is WORKER-scoped (control-channel only), so even a leaked
// plaintext warm bootstrap token can never resolve secrets — it names its pool and
// worker id and carries no task claims.
func TestWarmPodSpecFuncEnvVarFallbackIsWorkerScoped(t *testing.T) {
	cfg := &config.ServerConfig{} // transport unset => env-var fallback
	cfg.Execution.MaxWorkerLifetime = time.Hour
	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)

	spec, err := warmPodSpecFunc(cfg, authn, "cp:9000")(executor.WarmTarget{DagVersionID: "dv-1", Image: "reg/dag:v1", TenantID: "acme"})
	if err != nil {
		t.Fatalf("warmPodSpecFunc: %v", err)
	}
	if spec.BootstrapToken == "" {
		t.Fatal("env-var fallback must mint a plaintext bootstrap token")
	}
	id, verr := authn.AuthenticateAgent(spec.BootstrapToken)
	if verr != nil {
		t.Fatalf("bootstrap token does not verify: %v", verr)
	}
	if id.Scope != auth.ScopeWarmWorker {
		t.Errorf("bootstrap token scope = %q, want %q (control-channel only)", id.Scope, auth.ScopeWarmWorker)
	}
	if id.DagVersionID != "dv-1" || id.TenantID != "acme" {
		t.Errorf("bootstrap identity dag_version/tenant = %q/%q", id.DagVersionID, id.TenantID)
	}
	if id.TaskInstanceID != "" || id.RunID != "" || id.TaskID != "" || id.TryNumber != 0 {
		t.Errorf("warm bootstrap token must carry NO task claims, got %+v", *id)
	}
	if id.WorkerID == "" {
		t.Error("warm bootstrap token must carry a worker id (the Subject)")
	}
}
