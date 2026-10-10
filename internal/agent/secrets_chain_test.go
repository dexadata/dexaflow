package agent

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/agent/secretsource"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeResolver stands in for the pod-side external backend.
type fakeResolver struct {
	vars  map[string]string
	conns map[string]string
	err   error
}

func (f fakeResolver) Resolve(_ context.Context, name string, kind secretsource.Kind) (value string, found bool, err error) {
	if f.err != nil {
		return "", false, f.err
	}
	if kind == secretsource.KindConnection {
		v, ok := f.conns[name]
		return v, ok, nil
	}
	v, ok := f.vars[name]
	return v, ok, nil
}

// With no resolver configured, the chain is the vault only — byte-identical to the
// pre-ADR-0060 env-export.
func TestSecretsEnvNoResolverVaultOnly(t *testing.T) {
	r := &Runner{Client: &fakeClient{vars: map[string]string{"a": "1"}, conns: map[string]string{"db": "postgres://x"}}}
	out, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredVariables: []string{"a"}})
	if err != nil {
		t.Fatalf("secretsEnv: %v", err)
	}
	if !slices.Contains(out, "AIRFLOW_VAR_A=1") || !slices.Contains(out, "AIRFLOW_CONN_DB=postgres://x") {
		t.Errorf("vault-only env missing entries: %v", out)
	}
}

// A declared name the backend covers is resolved externally and OVERRIDES the
// vault entry for that name.
func TestSecretsEnvExternalOverridesVault(t *testing.T) {
	r := &Runner{
		Client:        &fakeClient{vars: map[string]string{"region": "vault-val"}},
		Resolver:      fakeResolver{vars: map[string]string{"region": "ext-val"}},
		SecretBackend: secretsource.Backend{Variables: true},
	}
	out, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredVariables: []string{"region"}})
	if err != nil {
		t.Fatalf("secretsEnv: %v", err)
	}
	if !slices.Contains(out, "AIRFLOW_VAR_REGION=ext-val") || slices.Contains(out, "AIRFLOW_VAR_REGION=vault-val") {
		t.Errorf("external hit must override vault: %v", out)
	}
}

// A hard resolver error (not a clean miss) fails the task closed (B6).
func TestSecretsEnvHardResolverErrorFailsClosed(t *testing.T) {
	r := &Runner{
		Client:        &fakeClient{},
		Resolver:      fakeResolver{err: errors.New("access denied")},
		SecretBackend: secretsource.Backend{Variables: true},
	}
	_, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredVariables: []string{"x"}})
	if err == nil {
		t.Fatal("a hard resolver error must fail the task closed")
	}
}

// A liveness-enforce PermissionDenied on a vault RPC skips external resolution
// entirely — a non-live TI resolves nothing (B2).
func TestSecretsEnvVaultDeniedSkipsExternal(t *testing.T) {
	r := &Runner{
		Client:        &fakeClient{getVarsErr: status.Error(codes.PermissionDenied, "task instance not live")},
		Resolver:      fakeResolver{vars: map[string]string{"x": "ext"}},
		SecretBackend: secretsource.Backend{Variables: true},
	}
	out, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{DeclaredVariables: []string{"x"}})
	if err != nil {
		t.Fatalf("secretsEnv (denied vault) should be best-effort, not error: %v", err)
	}
	if slices.Contains(out, "AIRFLOW_VAR_X=ext") {
		t.Error("external resolution must be skipped when the vault RPC is PermissionDenied (B2)")
	}
}

// fakeBatchResolver proves the chain prefers ONE batched call over per-name.
type fakeBatchResolver struct {
	hits  map[secretsource.Ref]string
	calls int
}

func (f *fakeBatchResolver) Resolve(context.Context, string, secretsource.Kind) (value string, found bool, err error) {
	return "", false, nil // must never be used when ResolveBatch is available
}

func (f *fakeBatchResolver) ResolveBatch(_ context.Context, refs []secretsource.Ref) (map[secretsource.Ref]string, error) {
	f.calls++
	out := map[secretsource.Ref]string{}
	for _, ref := range refs {
		if v, ok := f.hits[ref]; ok {
			out[ref] = v
		}
	}
	return out, nil
}

func TestSecretsEnvUsesBatchResolverOnce(t *testing.T) {
	br := &fakeBatchResolver{hits: map[secretsource.Ref]string{
		{Name: "region", Kind: secretsource.KindVariable}:      "us",
		{Name: "warehouse", Kind: secretsource.KindConnection}: "postgres://w",
	}}
	r := &Runner{
		Client:        &fakeClient{},
		Resolver:      br,
		SecretBackend: secretsource.Backend{Variables: true, Connections: true},
	}
	out, err := r.secretsEnv(context.Background(), &agentv1.TaskSpec{
		DeclaredVariables:   []string{"region", "sample"},
		DeclaredConnections: []string{"warehouse"},
	})
	if err != nil {
		t.Fatalf("secretsEnv: %v", err)
	}
	if br.calls != 1 {
		t.Errorf("expected ONE batched resolve for all covered names, got %d calls", br.calls)
	}
	if !slices.Contains(out, "AIRFLOW_VAR_REGION=us") || !slices.Contains(out, "AIRFLOW_CONN_WAREHOUSE=postgres://w") {
		t.Errorf("batched hits missing from env: %v", out)
	}
}

// A hard external-secret resolver error must fail the task with a terminal FAILED
// report (ADR 0060 B6) — not return before reporting, which would strand the TI as
// agent_lost with no visible cause.
func TestRunnerReportsFailedOnResolverError(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator: "bash", Entrypoint: "echo hi",
		DeclaredVariables: []string{"region"},
	}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.Resolver = fakeResolver{err: errors.New("access denied")}
	r.SecretBackend = secretsource.Backend{Variables: true}

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a hard resolver error must fail the task")
	}
	if len(client.states) == 0 || client.states[len(client.states)-1] != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("resolver error must report terminal FAILED (B6); states=%v", client.states)
	}
}

// A hard resolver error is the other diagnosis the agent classifies itself, so it
// travels in the durable outcome record too (#930): the task never ran, and if the
// FAILED report does not land the reconciler would otherwise render a bare
// "task failed (exit 1)" with no trace of the refused secret backend.
//
// The record's reason must be the CLASSIFICATION CONSTANT and nothing else. The
// field is durable and served to end users, so echoing the provider's own error
// text into it would turn it into an exfiltration path — the property
// taskoutcome.Record.Reason asserts, which only a classifier can keep.
func TestRunnerResolverErrorOutcomeRecordCarriesReason(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator: "bash", Entrypoint: "echo hi",
		DeclaredVariables: []string{"region"},
	}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.TerminationLogPath = path
	r.Resolver = fakeResolver{err: errors.New("access denied by sts.example.internal")}
	r.SecretBackend = secretsource.Backend{Variables: true}

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a hard resolver error must fail the task")
	}
	rec := readOutcome(t, path)
	if rec.Reason != reasonSecretUnresolved {
		t.Errorf("record reason = %q, want the classification constant %q", rec.Reason, reasonSecretUnresolved)
	}
	if strings.Contains(rec.Reason, "access denied") || strings.Contains(rec.Reason, "sts.example.internal") {
		t.Errorf("record reason leaks the provider's raw error text: %q", rec.Reason)
	}
}
