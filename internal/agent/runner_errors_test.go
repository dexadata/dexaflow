package agent

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

func TestNoopLogSink(t *testing.T) {
	var s NoopLogSink
	if err := s.Send(&agentv1.LogLine{Message: "x"}); err != nil {
		t.Errorf("NoopLogSink.Send should discard, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("NoopLogSink.Close should be a no-op, got %v", err)
	}
}

// registerErrClient overrides Register to fail; all other RPCs use fakeClient.
type registerErrClient struct{ *fakeClient }

func (registerErrClient) Register(context.Context, *agentv1.RegisterRequest, ...grpc.CallOption) (*agentv1.RegisterResponse, error) {
	return nil, errors.New("control plane unreachable")
}

// TestRunnerFailsWhenRegisterFails: if the agent cannot register, the run must
// abort up front rather than running the task unregistered.
func TestRunnerFailsWhenRegisterFails(t *testing.T) {
	base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:x"}}
	r := newRunner(base, &fakeCmd{}, &recordingSink{})
	r.Client = registerErrClient{base}
	if err := r.Run(context.Background()); err == nil {
		t.Error("a register failure should abort the run")
	}
}

// secretsErrClient overrides the secret RPCs to fail.
type secretsErrClient struct{ *fakeClient }

func (secretsErrClient) GetVariables(context.Context, *agentv1.GetVariablesRequest, ...grpc.CallOption) (*agentv1.GetVariablesResponse, error) {
	return nil, errors.New("secrets refused on insecure channel")
}

func (secretsErrClient) GetConnections(context.Context, *agentv1.GetConnectionsRequest, ...grpc.CallOption) (*agentv1.GetConnectionsResponse, error) {
	return nil, errors.New("secrets refused on insecure channel")
}

// TestSecretsEnvToleratesFetchErrors: Variables/Connections are best-effort
// (ADR 0021) — a fetch failure must not fail the task; buildEnv still succeeds
// (the task simply runs without those env secrets).
func TestSecretsEnvToleratesFetchErrors(t *testing.T) {
	base := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:x"}}
	r := newRunner(base, &fakeCmd{}, &recordingSink{})
	r.Client = secretsErrClient{base}
	if _, err := r.buildEnv(context.Background(), base.spec); err != nil {
		t.Errorf("secret-fetch errors must be tolerated, got %v", err)
	}
}
