package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Reasons an agent bearer token is rejected (#1125). They are a closed set so a
// log line or a metric label built from one never carries anything derived from
// the token itself. Each points the operator at a different fault:
//
//   - expired: the credential lapsed. A running attempt renews on every
//     heartbeat, so this means the agent could not reach the control plane for a
//     whole TTL, or its pod started after the credential minted at dispatch had
//     already run out.
//   - wrong_audience: a token minted for another surface (a user token) reached
//     the agent service: a wiring bug.
//   - bad_signature: the token was signed with another key (key rotation, or a
//     token from another install) or with a method the server does not accept.
//   - wrong_issuer, not_yet_valid, malformed: the token is not one this control
//     plane minted as it stands.
//   - invalid: any other verification failure.
const (
	AgentTokenExpired       = "expired"
	AgentTokenWrongAudience = "wrong_audience"
	AgentTokenBadSignature  = "bad_signature"
	AgentTokenWrongIssuer   = "wrong_issuer"
	AgentTokenNotYetValid   = "not_yet_valid"
	AgentTokenMalformed     = "malformed"
	AgentTokenInvalid       = "invalid"
)

// AgentTokenRejection describes why AuthenticateAgent refused a bearer token.
// AuthenticateAgent joins it into its error (alongside ErrInvalidToken), so a
// caller recovers it with errors.As.
//
// It never holds the token or any part of it. Identity is set only when the
// token's signature, issuer and audience all verified and the token failed on
// time alone (expired or not yet valid): its claims are then authentic and name
// the attempt the credential belonged to. For any other reason the claims are
// unauthenticated or belong to another surface, so they are not surfaced.
type AgentTokenRejection struct {
	// Reason is one of the AgentToken* reason constants.
	Reason string
	// Identity is the attempt an authentic but out-of-window token belonged to,
	// or nil.
	Identity *AgentIdentity
	// ExpiredAt is when an expired token lapsed; zero for any other reason.
	ExpiredAt time.Time
}

// Error names the reason only.
func (r *AgentTokenRejection) Error() string {
	return "agent token rejected: " + r.Reason
}

// agentTokenRejection classifies a verification error from jwt.ParseWithClaims
// on an agent token. c holds whatever claims the parser decoded; they are read
// only for a time-window failure, which jwt/v5 reports after the signature,
// issuer and audience have verified.
func agentTokenRejection(err error, c *agentClaims) *AgentTokenRejection {
	rej := &AgentTokenRejection{Reason: classifyAgentTokenError(err)}
	if rej.Reason != AgentTokenExpired && rej.Reason != AgentTokenNotYetValid {
		return rej
	}
	id := identityFromClaims(*c)
	rej.Identity = &id
	if rej.Reason == AgentTokenExpired && c.ExpiresAt != nil {
		rej.ExpiredAt = c.ExpiresAt.Time
	}
	return rej
}

// classifyAgentTokenError maps a jwt verification error to a reason. A token
// can fail several checks at once; the order names the most specific cause
// first, so an expired user token reads as the wiring bug it is rather than as
// a lapse.
func classifyAgentTokenError(err error) string {
	switch {
	case errors.Is(err, jwt.ErrTokenMalformed):
		return AgentTokenMalformed
	case errors.Is(err, jwt.ErrTokenSignatureInvalid), errors.Is(err, jwt.ErrTokenUnverifiable):
		return AgentTokenBadSignature
	case errors.Is(err, jwt.ErrTokenInvalidAudience):
		return AgentTokenWrongAudience
	case errors.Is(err, jwt.ErrTokenInvalidIssuer):
		return AgentTokenWrongIssuer
	case errors.Is(err, jwt.ErrTokenExpired):
		return AgentTokenExpired
	case errors.Is(err, jwt.ErrTokenNotValidYet), errors.Is(err, jwt.ErrTokenUsedBeforeIssued):
		return AgentTokenNotYetValid
	default:
		return AgentTokenInvalid
	}
}
