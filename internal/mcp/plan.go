package mcp

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// planTTL is how long a plan can be applied (ADR 0067).
const planTTL = 10 * time.Minute

// PlanKeyMinBytes is the shortest plan key LoadPlanKey accepts.
const PlanKeyMinBytes = 32

// maxPlanTaskInstances bounds a clear plan. A plan carries every task
// instance it will clear, so an unbounded one would not fit a tool argument;
// a wider clear belongs in the UI.
const maxPlanTaskInstances = 200

// LoadPlanKey reads the HMAC key that signs plans (ADR 0067) from path: at
// least PlanKeyMinBytes bytes once a trailing newline is dropped. Every
// replica of the HTTP transport must load the same key, so a plan made on one
// applies on another.
func LoadPlanKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // the operator names the file
	if err != nil {
		return nil, fmt.Errorf("plan key: %w", err)
	}
	key := bytes.TrimRight(raw, "\r\n")
	if len(key) < PlanKeyMinBytes {
		return nil, fmt.Errorf("plan key: %d bytes, want at least %d", len(key), PlanKeyMinBytes)
	}
	return key, nil
}

// randomPlanKey is the stdio transport's key: one local caller, one process.
func randomPlanKey() []byte {
	key := make([]byte, PlanKeyMinBytes)
	_, _ = rand.Read(key) // crypto/rand.Read never fails on supported platforms
	return key
}

// planTI is one task instance a clear plan will clear, with its state when
// the plan was made.
type planTI struct {
	TaskID   string `json:"t"`
	MapIndex int    `json:"m"`
	State    string `json:"s"`
	Try      int    `json:"n,omitempty"`
}

// plan is a risky operation waiting for apply_plan. It travels inside its
// plan_id, signed, so the stateless HTTP transport needs no plan store: the
// model can only hand it back, never change what it does.
type plan struct {
	Action   string       `json:"a"` // planClear or planUnpause
	DagID    string       `json:"d"`
	Clear    *clearParams `json:"c,omitempty"`
	Expect   []planTI     `json:"e,omitempty"` // clear: the preview the user saw
	Schedule string       `json:"s,omitempty"` // unpause: the schedule the user saw
	Caller   string       `json:"w"`           // callerKey of whoever planned it
	Expires  int64        `json:"x"`           // unix seconds
}

const (
	planClear   = "clear"
	planUnpause = "unpause"
)

// sealPlan signs p with key: base64url(JSON) "." base64url(HMAC-SHA256).
func sealPlan(key []byte, p plan) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encoding plan: %w", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(b)
	return payload + "." + base64.RawURLEncoding.EncodeToString(planMAC(key, payload)), nil
}

func planMAC(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}

// openPlan verifies a plan_id's signature, expiry and caller.
func openPlan(key []byte, id, caller string, now time.Time) (plan, error) {
	payload, sig, ok := strings.Cut(id, ".")
	if !ok {
		return plan{}, errors.New("not a plan_id")
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, planMAC(key, payload)) {
		return plan{}, errors.New("plan_id signature does not verify; make a new plan")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return plan{}, fmt.Errorf("decoding plan: %w", err)
	}
	var p plan
	if err := json.Unmarshal(raw, &p); err != nil {
		return plan{}, fmt.Errorf("decoding plan: %w", err)
	}
	if now.Unix() > p.Expires {
		return plan{}, errors.New("plan expired; make a new plan")
	}
	if !hmac.Equal([]byte(p.Caller), []byte(caller)) {
		return plan{}, errors.New("plan was made by another user, tenant or client")
	}
	return p, nil
}

// unboundClaims are the claims that change when a client refreshes its token
// for the same caller, so they do not take part in callerKey.
var unboundClaims = []string{"iat", "exp", "nbf", "jti", "auth_time"}

// callerKey names who is calling, to bind a plan to them. On stdio there is
// one local caller. Over HTTP it is a hash of the bearer's claims minus the
// ones a refresh changes, so it covers the tenant (whatever its claim's name),
// the subject, the client and the scopes. The claims are read unverified: the
// control plane verifies the token of every call, apply included, so a forged
// token fails there.
func (h *handlers) callerKey(req *mcpsdk.CallToolRequest) (string, error) {
	if !h.requireBearer {
		return "stdio", nil
	}
	var token string
	if req != nil && req.Extra != nil && req.Extra.Header != nil {
		token = strings.TrimSpace(strings.TrimPrefix(req.Extra.Header.Get("Authorization"), "Bearer "))
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("cannot bind a plan to this caller: the bearer is not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("cannot bind a plan to this caller: %w", err)
	}
	var claims map[string]any
	if err = json.Unmarshal(raw, &claims); err != nil {
		return "", fmt.Errorf("cannot bind a plan to this caller: %w", err)
	}
	for _, c := range unboundClaims {
		delete(claims, c)
	}
	canon, err := json.Marshal(claims) // map keys marshal sorted
	if err != nil {
		return "", fmt.Errorf("cannot bind a plan to this caller: %w", err)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}
