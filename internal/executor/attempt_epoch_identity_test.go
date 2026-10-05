package executor

import (
	"encoding/json"
	"testing"
)

// TestBuildPodIdentityAnnotationCarriesAttemptEpoch: under the exchange
// transport the identity annotation carries the attempt epoch the dispatcher
// claimed, so the exchanged JWT is minted for this execution and not just for
// its try (ADR 0051 amendment).
func TestBuildPodIdentityAnnotationCarriesAttemptEpoch(t *testing.T) {
	req := sampleReq()
	req.AgentTokenTransport = "exchange"
	req.AttemptEpoch = 4
	pod := BuildPod(req)
	raw, ok := pod.Annotations[AgentIdentityAnnotation]
	if !ok {
		t.Fatal("exchange: identity annotation not set")
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		t.Fatalf("annotation is not JSON: %v", err)
	}
	if wire["epoch"] != float64(4) {
		t.Errorf("annotation epoch = %v, want 4 (raw %s)", wire["epoch"], raw)
	}
	id, err := ParseAgentIdentity(raw)
	if err != nil {
		t.Fatalf("ParseAgentIdentity: %v", err)
	}
	if id.AttemptEpoch == nil || *id.AttemptEpoch != 4 {
		t.Errorf("parsed epoch = %v, want 4", id.AttemptEpoch)
	}
}

// TestParseAgentIdentityDistinguishesAbsentEpoch: a pod created before the
// epoch existed has no "epoch" field and parses with a nil epoch (the legacy
// rule then treats it as 0), while an explicit 0 parses as present.
func TestParseAgentIdentityDistinguishesAbsentEpoch(t *testing.T) {
	legacy, err := ParseAgentIdentity(`{"ti":"ti-1","tenant":"acme","dag":"etl","run":"r1","task":"t","try":2}`)
	if err != nil {
		t.Fatalf("ParseAgentIdentity(legacy): %v", err)
	}
	if legacy.AttemptEpoch != nil {
		t.Errorf("a legacy annotation must parse with no epoch, got %d", *legacy.AttemptEpoch)
	}
	zero, err := ParseAgentIdentity(`{"ti":"ti-1","try":2,"epoch":0}`)
	if err != nil {
		t.Fatalf("ParseAgentIdentity(zero): %v", err)
	}
	if zero.AttemptEpoch == nil || *zero.AttemptEpoch != 0 {
		t.Errorf("an explicit epoch 0 must parse as present 0, got %v", zero.AttemptEpoch)
	}
}
