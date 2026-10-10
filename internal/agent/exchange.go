package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// ExchangeToken performs the one-time bootstrap token exchange (ADR 0055 Fix #3):
// it calls the control plane's ExchangeToken RPC carrying the current bootstrap
// bearer (the projected ServiceAccount token) and swaps the returned task-scoped
// agent JWT into tokens, so every subsequent RPC authenticates as the task
// instance. It runs ONLY under the exchange transport, before any other RPC; the
// default env-var transport never calls it.
//
// It fails the startup on any error: the agent must never proceed with a
// bootstrap credential the control plane rejected, nor with an empty token.
func ExchangeToken(ctx context.Context, client agentv1.AgentServiceClient, tokens *TokenSource) error {
	resp, err := client.ExchangeToken(ctx, &agentv1.ExchangeTokenRequest{})
	if err != nil {
		return fmt.Errorf("exchanging bootstrap token: %w", err)
	}
	minted := resp.GetAgentToken()
	if minted == "" {
		return errors.New("token exchange returned an empty agent token")
	}
	tokens.Set(minted)
	return nil
}

// ReadTokenFile reads a projected token from path and trims surrounding
// whitespace (the kubelet writes the token without a trailing newline, but trim
// defensively so the bearer matches exactly what the apiserver signed).
func ReadTokenFile(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: token path is set by the control plane on the pod spec.
	if err != nil {
		return "", fmt.Errorf("reading projected token %q: %w", path, err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("projected token file %q is empty", path)
	}
	return token, nil
}
