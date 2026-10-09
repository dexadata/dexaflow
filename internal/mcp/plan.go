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
	"net/http"
	"os"
	"slices"
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
	Version  int          `json:"v"` // planVersion; another version is refused
	Action   string       `json:"a"` // planClear or planUnpause
	DagID    string       `json:"d"`
	Clear    *clearParams `json:"c,omitempty"`
	Expect   []planTI     `json:"e,omitempty"` // clear: the preview the user saw
	Schedule string       `json:"s,omitempty"` // unpause: the schedule the user saw
	Caller   string       `json:"w"`           // callerKey of whoever planned it
	Expires  int64        `json:"x"`           // unix seconds
}

// planVersion is the plan_id format sealPlan writes and openPlan accepts.
const planVersion = 1

const (
	planClear   = "clear"
	planUnpause = "unpause"
)

// sealPlan signs p with key: base64url(JSON) "." base64url(HMAC-SHA256).
func sealPlan(key []byte, p plan) (string, error) {
	p.Version = planVersion
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
	if p.Version != planVersion {
		return plan{}, fmt.Errorf("plan_id has format version %d, this server reads %d; make a new plan", p.Version, planVersion)
	}
	if now.Unix() > p.Expires {
		return plan{}, errors.New("plan expired; make a new plan")
	}
	if !hmac.Equal([]byte(p.Caller), []byte(caller)) {
		return plan{}, errors.New("plan was made by another caller (issuer, subject, tenant, client or scopes differ); make a new plan")
	}
	return p, nil
}

// tenantClaims are the claims, in order, that name the caller's tenant: the
// engine's own tokens carry tenant_id; a trusted issuer's carry the claim its
// operator configured, commonly one of the others.
var tenantClaims = []string{"tenant_id", "tenant", "tid"}

// planCaller is who a plan is bound to (ADR 0067): the token's issuer,
// subject, tenant, client (azp, else client_id) and scope set. Nothing else
// in the token takes part, so a refresh, or a change of roles or email, keeps
// the plan; the control plane re-checks the caller's permissions on every
// apply call anyway.
type planCaller struct {
	Issuer  string   `json:"iss"`
	Subject string   `json:"sub"`
	Tenant  string   `json:"tenant"`
	Client  string   `json:"client"`
	Scopes  []string `json:"scope"`
}

// bearerToken returns the token of an "Authorization: Bearer" header, the
// scheme matched in any case (RFC 7235), or "".
func bearerToken(header http.Header) string {
	if header == nil {
		return ""
	}
	fields := strings.Fields(header.Get("Authorization"))
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	return fields[1]
}

// callerKey names who is calling, to bind a plan to them. On stdio there is
// one local caller. Over HTTP it is a hash of the bearer's planCaller. The
// claims are read unverified: the control plane verifies the token of every
// call, apply included, so a forged token fails there.
func (h *handlers) callerKey(req *mcpsdk.CallToolRequest) (string, error) {
	if !h.requireBearer {
		return "stdio", nil
	}
	var token string
	if req != nil && req.Extra != nil {
		token = bearerToken(req.Extra.Header)
	}
	claims, err := jwtClaims(token)
	if err != nil {
		return "", fmt.Errorf("cannot bind a plan to this caller: %w", err)
	}
	c, err := callerOf(claims)
	if err != nil {
		return "", fmt.Errorf("cannot bind a plan to this caller: %w", err)
	}
	canon, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("cannot bind a plan to this caller: %w", err)
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// jwtClaims decodes, without verifying, the claims of a three-part JWT.
func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("the bearer is not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// callerOf reads the planCaller out of claims. Issuer, subject and tenant are
// required; a token without a client or a scope binds to their absence.
func callerOf(claims map[string]any) (planCaller, error) {
	str := func(name string) string {
		v, _ := claims[name].(string)
		return v
	}
	c := planCaller{Issuer: str("iss"), Subject: str("sub"), Client: str("azp")}
	if c.Client == "" {
		c.Client = str("client_id")
	}
	for _, name := range tenantClaims {
		if c.Tenant = str(name); c.Tenant != "" {
			break
		}
	}
	switch {
	case c.Issuer == "":
		return planCaller{}, errors.New("the token has no iss claim")
	case c.Subject == "":
		return planCaller{}, errors.New("the token has no sub claim")
	case c.Tenant == "":
		return planCaller{}, fmt.Errorf("the token names no tenant (claims %s)", strings.Join(tenantClaims, ", "))
	}
	c.Scopes = scopeSet(claims)
	return c, nil
}

// scopeSet is the token's scopes, from a space-separated "scope" claim or an
// "scp" list, sorted and without duplicates, so their order does not matter.
func scopeSet(claims map[string]any) []string {
	var out []string
	if s, ok := claims["scope"].(string); ok {
		out = strings.Fields(s)
	}
	switch v := claims["scp"].(type) {
	case string:
		out = append(out, strings.Fields(v)...)
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
