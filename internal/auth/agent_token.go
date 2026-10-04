package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// audienceAgent scopes tokens minted for the in-pod agent. A token with this
// audience identifies exactly one task instance and cannot be used on the
// user-facing API (see audienceUser).
const audienceAgent = "leoflow-agent"

// ScopeWarmWorker marks an agent token that authorizes ONLY the warm-worker
// control channel — Register + AwaitAssignment — and NOTHING that resolves
// secrets or a task (ADR 0058 D2). It is the value of AgentIdentity.Scope for a
// warm worker's bootstrap credential; the empty scope is the default task
// credential, which resolves secrets exactly as before. The control plane gates
// every secret/task RPC on this value (see agentrpc.requireAttemptToken).
const ScopeWarmWorker = "warm-worker"

// AgentIdentity is the identity a verified agent token represents. By default
// (Scope == "") it is a single task instance — the task-scoped credential that
// resolves secrets. When Scope == ScopeWarmWorker it is instead a warm worker's
// bootstrap credential, which names its dag_version pool (DagVersionID) and its
// worker id (WorkerID, the token Subject) and carries NO task claims.
type AgentIdentity struct {
	TaskInstanceID string
	TenantID       string
	DagID          string
	RunID          string
	TaskID         string
	TryNumber      int
	// AttemptEpoch identifies which execution of TryNumber this credential
	// belongs to (ADR 0051 amendment): an infra re-place, a reschedule poke or a
	// repeated dispatch reuses the try but never the epoch. It is meaningful
	// only when HasAttemptEpoch is set.
	AttemptEpoch int
	// HasAttemptEpoch is the presence bit for AttemptEpoch. It is false for a
	// token minted before the epoch existed (a legacy token, which the fence
	// treats as epoch 0) and for a warm-worker credential, so "absent" stays
	// distinguishable from epoch 0.
	HasAttemptEpoch bool
	// Scope is "" for a task credential (the default, byte-compatible with every
	// token minted before this field existed) or ScopeWarmWorker for a warm
	// worker's control-channel-only bootstrap credential.
	Scope string
	// DagVersionID names the warm pool a warm-worker credential serves. Empty on a
	// task credential.
	DagVersionID string
	// WorkerID is the warm worker's stable identifier (its pod name), minted as the
	// token Subject for a warm-worker credential. Empty on a task credential (whose
	// Subject is TaskInstanceID instead).
	WorkerID string
}

// agentClaims is the JWT payload of an agent identity token.
type agentClaims struct {
	TenantID  string `json:"tenant_id"`
	DagID     string `json:"dag_id"`
	RunID     string `json:"run_id"`
	TaskID    string `json:"task_id"`
	TryNumber int    `json:"try_number"`
	// Scope and DagVersionID describe a warm-worker credential (ADR 0058 D2). Both
	// are omitempty so a task credential's payload is byte-identical to before these
	// fields existed — an existing task token still verifies unchanged, and a freshly
	// minted task token carries neither claim.
	Scope        string `json:"scope,omitempty"`
	DagVersionID string `json:"dag_version_id,omitempty"`
	// OriginIssuedAt records when this attempt's credential was FIRST minted (at
	// dispatch). Unlike iat, it is preserved verbatim across heartbeat renewals so
	// the control plane can bound an attempt's total credential lifetime no matter
	// how many times its token is re-minted (ADR 0055 Fix #4). Absent on tokens
	// minted before this field existed; renewal then falls back to iat.
	OriginIssuedAt *jwt.NumericDate `json:"oiat,omitempty"`
	// AttemptEpoch is the execution of try_number this task credential belongs
	// to (ADR 0051 amendment). A pointer so that "absent" (a token minted before
	// the claim existed, or a warm-worker credential) is distinguishable from
	// epoch 0; omitempty keeps an absent claim off the wire, so renewal
	// preserves the absence instead of writing a zero.
	AttemptEpoch *int64 `json:"attempt_epoch,omitempty"`
	jwt.RegisteredClaims
}

// IssueAgentToken mints a signed token that identifies a single task instance,
// valid for the given TTL. The control plane passes it to the worker pod.
//
// The token's origin (oiat) is set to the mint time — this is a fresh dispatch.
// Renewal (RenewAgentToken) preserves that origin instead of resetting it.
func (a *JWTAuthenticator) IssueAgentToken(id AgentIdentity, ttl time.Duration) (string, error) {
	return a.mintAgentToken(id, ttl, a.clock())
}

// mintAgentToken signs an agent token whose iat/exp are anchored at now and whose
// preserved dispatch-origin is `origin`. Dispatch passes now as the origin; a
// renewal passes the incoming token's origin so it never advances.
func (a *JWTAuthenticator) mintAgentToken(id AgentIdentity, ttl time.Duration, origin time.Time) (string, error) {
	now := a.clock()
	// The Subject is the worker id for a warm-worker credential and the task
	// instance id for a task credential. A warm-worker identity carries no task
	// claims (RunID/TaskID/TryNumber empty), so those fields serialize empty —
	// the worker credential names only its pool (dag_version_id) and its scope.
	subject := id.TaskInstanceID
	if id.Scope == ScopeWarmWorker {
		subject = id.WorkerID
	}
	c := agentClaims{
		TenantID:       id.TenantID,
		DagID:          id.DagID,
		RunID:          id.RunID,
		TaskID:         id.TaskID,
		TryNumber:      id.TryNumber,
		Scope:          id.Scope,
		DagVersionID:   id.DagVersionID,
		OriginIssuedAt: jwt.NewNumericDate(origin),
		AttemptEpoch:   epochClaim(id),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			Issuer:    tokenIssuer,
			Audience:  jwt.ClaimStrings{audienceAgent},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(a.secret)
	if err != nil {
		return "", fmt.Errorf("signing agent token: %w", err)
	}
	return signed, nil
}

// RenewAgentToken validates an in-flight agent bearer token and re-mints it for
// the SAME task instance with a fresh short TTL, refreshing the live window
// without ever changing what AuthenticateAgent verifies (signature, issuer,
// leoflow-agent audience, HS256). It is the heartbeat-driven half of the
// short-TTL-plus-renewal design (ADR 0055 Fix #4): the short TTL bounds a
// stolen or finished token, while renewal keeps a genuinely live task's
// credential working.
//
// The attempt's original dispatch time is preserved across every renewal (the
// oiat claim). maxLifetime is a hard ceiling on that total age: once the attempt
// has been alive longer than maxLifetime since first dispatch, renewal is
// refused (ok=false, empty token) so a runaway attempt's credential lapses
// instead of being kept alive forever. A non-positive maxLifetime disables the
// ceiling. exp is always now+ttl — never accumulated onto the previous exp.
//
// An invalid incoming token (bad signature, wrong audience, expired) returns an
// error and is never re-minted.
func (a *JWTAuthenticator) RenewAgentToken(token string, ttl, maxLifetime time.Duration) (renewed string, ok bool, err error) {
	var c agentClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceAgent), jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(a.clock))
	if err != nil || !parsed.Valid {
		return "", false, errors.Join(ErrInvalidToken, err)
	}
	// Preserve the attempt's first-dispatch origin across renewals; fall back to
	// iat for a token minted before the origin claim existed.
	origin := time.Time{}
	if c.OriginIssuedAt != nil {
		origin = c.OriginIssuedAt.Time
	} else if c.IssuedAt != nil {
		origin = c.IssuedAt.Time
	}
	if maxLifetime > 0 && !origin.IsZero() && a.clock().Sub(origin) > maxLifetime {
		return "", false, nil // past the ceiling: let the credential lapse
	}
	renewed, err = a.mintAgentToken(identityFromClaims(c), ttl, origin)
	if err != nil {
		return "", false, err
	}
	return renewed, true, nil
}

// AuthenticateAgent validates an agent bearer token and returns the task
// instance it identifies.
func (a *JWTAuthenticator) AuthenticateAgent(token string) (*AgentIdentity, error) {
	var c agentClaims
	parsed, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) {
		return a.secret, nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithAudience(audienceAgent), jwt.WithValidMethods([]string{"HS256"}), jwt.WithTimeFunc(a.clock))
	if err != nil || !parsed.Valid {
		return nil, errors.Join(ErrInvalidToken, err)
	}
	id := identityFromClaims(c)
	return &id, nil
}

// identityFromClaims reconstructs the AgentIdentity a verified token represents,
// routing the Subject to WorkerID for a warm-worker credential and to
// TaskInstanceID for a task credential. It is the single read side shared by
// AuthenticateAgent and RenewAgentToken so a warm-worker token is never silently
// downgraded to a task token on re-mint.
func identityFromClaims(c agentClaims) AgentIdentity {
	id := AgentIdentity{
		TenantID:     c.TenantID,
		DagID:        c.DagID,
		RunID:        c.RunID,
		TaskID:       c.TaskID,
		TryNumber:    c.TryNumber,
		Scope:        c.Scope,
		DagVersionID: c.DagVersionID,
	}
	if c.Scope == ScopeWarmWorker {
		id.WorkerID = c.Subject
	} else {
		id.TaskInstanceID = c.Subject
	}
	if c.AttemptEpoch != nil {
		id.AttemptEpoch = int(*c.AttemptEpoch)
		id.HasAttemptEpoch = true
	}
	return id
}

// epochClaim is the attempt_epoch claim for an identity: nil (absent) unless
// the identity carries an epoch. Only a task credential can: a warm-worker
// credential names no attempt. Because identityFromClaims sets the presence bit
// only when the claim was there, a renewal re-mints exactly what it was given
// and never upgrades a legacy token to an epoch.
func epochClaim(id AgentIdentity) *int64 {
	if !id.HasAttemptEpoch || id.Scope == ScopeWarmWorker {
		return nil
	}
	n := int64(id.AttemptEpoch)
	return &n
}
