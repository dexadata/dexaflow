package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// exchangeClient overrides ExchangeToken to return a configured token or error;
// all other RPCs fall back to fakeClient.
type exchangeClient struct {
	*fakeClient
	token string
	err   error
	calls int
}

func (c *exchangeClient) ExchangeToken(context.Context, *agentv1.ExchangeTokenRequest, ...grpc.CallOption) (*agentv1.ExchangeTokenResponse, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return &agentv1.ExchangeTokenResponse{AgentToken: c.token}, nil
}

// TestExchangeTokenSwapsBearer: the agent exchanges its bootstrap (projected)
// token for the returned task-scoped JWT and swaps it into the shared
// TokenSource, so every subsequent RPC authenticates as the task instance.
func TestExchangeTokenSwapsBearer(t *testing.T) {
	tokens := NewTokenSource("projected-bootstrap")
	client := &exchangeClient{fakeClient: &fakeClient{}, token: "task-scoped-jwt"}

	if err := ExchangeToken(context.Background(), client, tokens); err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if got := tokens.Token(); got != "task-scoped-jwt" {
		t.Errorf("bearer after exchange = %q, want the task-scoped JWT", got)
	}
	if client.calls != 1 {
		t.Errorf("ExchangeToken called %d times, want exactly once", client.calls)
	}
}

// TestExchangeTokenSurfacesError: an exchange failure aborts startup — the agent
// must never proceed with a bootstrap credential the control plane rejected.
func TestExchangeTokenSurfacesError(t *testing.T) {
	tokens := NewTokenSource("projected-bootstrap")
	client := &exchangeClient{fakeClient: &fakeClient{}, err: errors.New("projected token is not valid")}

	if err := ExchangeToken(context.Background(), client, tokens); err == nil {
		t.Error("a rejected exchange must return an error")
	}
	if got := tokens.Token(); got != "projected-bootstrap" {
		t.Errorf("bearer must be unchanged after a failed exchange, got %q", got)
	}
}

// TestExchangeTokenRejectsEmpty: an empty returned token is a protocol fault, not
// a working credential — fail rather than silently keep the bootstrap token.
func TestExchangeTokenRejectsEmpty(t *testing.T) {
	tokens := NewTokenSource("projected-bootstrap")
	client := &exchangeClient{fakeClient: &fakeClient{}, token: ""}
	if err := ExchangeToken(context.Background(), client, tokens); err == nil {
		t.Error("an empty exchanged token must return an error")
	}
}

// TestReadTokenFileTrims: the projected token file is read and trailing
// whitespace/newlines trimmed so the bearer matches what the apiserver signed.
func TestReadTokenFileTrims(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("the-projected-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadTokenFile(path)
	if err != nil {
		t.Fatalf("ReadTokenFile: %v", err)
	}
	if got != "the-projected-token" {
		t.Errorf("ReadTokenFile = %q, want trimmed token", got)
	}
}

// TestReadTokenFileMissing: a missing file is an error (the exchange cannot run).
func TestReadTokenFileMissing(t *testing.T) {
	if _, err := ReadTokenFile(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("ReadTokenFile on a missing path must error")
	}
}
