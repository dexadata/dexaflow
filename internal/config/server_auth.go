package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// TrustedIssuerSection configures one trusted external issuer (#1284). Its
// tokens are verified against its published JWKS and name an existing user,
// linked by (issuer:<name>, subject), in an allowed tenant; they never create
// users or grant roles.
type TrustedIssuerSection struct {
	// Name identifies the issuer; its users are linked under "issuer:<name>".
	// Lowercase letters, digits and '-'. Keep it stable once users exist.
	Name string `mapstructure:"name"`
	// Issuer is the exact `iss` the tokens carry.
	Issuer string `mapstructure:"issuer"`
	// JWKSURL is where the issuer publishes its public signing keys: https, or
	// http on a loopback host for local development.
	JWKSURL string `mapstructure:"jwks_url"`
	// Audience is the `aud` the tokens must carry for this Dexaflow.
	Audience string `mapstructure:"audience"`
	// TenantClaim names the string claim carrying the Dexaflow tenant name.
	TenantClaim string `mapstructure:"tenant_claim"`
	// AllowedTenants lists the tenants the issuer may sign in to; "*" allows
	// every tenant.
	AllowedTenants []string `mapstructure:"allowed_tenants"`
	// MaxLifetimeSeconds caps exp - iat of a token, the replay window of a
	// handoff. Zero uses the 120-second default; at most 600.
	MaxLifetimeSeconds int `mapstructure:"max_lifetime_seconds"`
	// AllowedOrigins are the origins (scheme://host[:port]) whose pages may
	// post a handoff. Any other Origin, or none, is refused, so another site
	// cannot sign a visitor in (login CSRF). Required.
	AllowedOrigins []string `mapstructure:"allowed_origins"`
	// BearerAudiences turns on the bearer mode (#1468): a token whose `aud` is
	// one of these is accepted as the bearer of any /api/v2 request, reused
	// within its lifetime, the way a remote MCP client authenticates (ADR 0050
	// D9). Each must differ from Audience, so a handoff token is never a
	// bearer. Empty (the default) leaves the mode off.
	BearerAudiences []string `mapstructure:"bearer_audiences"`
	// BearerMaxLifetimeSeconds caps exp - iat of a bearer. Zero uses the
	// 900-second default; at most 3600.
	BearerMaxLifetimeSeconds int `mapstructure:"bearer_max_lifetime_seconds"`
	// DeployAudience makes the issuer the only way to deploy (#1773). When
	// set, registering a DAG version (and its dry run) and setting the current
	// version, and deleting a DAG, take only a bearer of this issuer whose
	// `aud` is this audience alone and that carries dexaflow:deploy, besides
	// the role check; a token that also names another audience, a session, a
	// password login token, an API token and a bearer of another audience
	// are refused with 403. It is a bearer audience of its own: it turns the
	// bearer verifier on without BearerAudiences, and a token for it gets
	// the scopes it carries, like any bearer. It must differ from Audience
	// and from every BearerAudiences entry, so a handoff or another client's
	// bearer is never a deploy token. Empty (the default) changes nothing.
	DeployAudience string `mapstructure:"deploy_audience"`
	// HostPattern binds a browser session to its tenant's host (#1463), for
	// an operator that serves each tenant on its own host in front of one
	// engine, for example "{tenant}.example.com". When set, a handoff is
	// accepted only on the host the pattern names for the token's tenant, and
	// a request that authenticates with the session cookie only on the host
	// for the session's tenant. The host is the request's Host, or
	// X-Forwarded-Host from a peer in server.trusted_proxies. A bearer in the
	// Authorization header is not bound. Empty (the default) binds nothing.
	HostPattern string `mapstructure:"host_pattern"`
}

// HostPatternTenant is the placeholder auth.trusted_issuer.host_pattern
// replaces with the session's tenant.
const HostPatternTenant = "{tenant}"

// Enabled reports whether a trusted issuer is configured.
func (s TrustedIssuerSection) Enabled() bool { return s.Issuer != "" }

// AuthSection configures authentication.
type AuthSection struct {
	Provider string     `mapstructure:"provider"`
	JWT      JWTSection `mapstructure:"jwt"`
	// OIDC configures the OIDC/SSO login flow. It is read only when Provider is
	// "oidc" (Pro-gated); the JWT authenticator remains the request-path verifier
	// in both modes.
	OIDC OIDCSection `mapstructure:"oidc"`
	// TrustedIssuer lets a platform that already authenticates its users open a
	// UI session for them with a token its own issuer signed (#1284). Empty
	// Issuer disables it.
	TrustedIssuer TrustedIssuerSection `mapstructure:"trusted_issuer"`
	// CIOIDC lets CI pipelines deploy with their provider's OIDC token
	// instead of a stored deploy token (ADR 0092, #1882). No issuer turns
	// it off.
	CIOIDC CIOIDCSection `mapstructure:"ci_oidc"`
	// APITokens can turn the long-lived API tokens (ADR 0074) off.
	APITokens APITokensSection `mapstructure:"api_tokens"`
	// ServiceToken enables the operator service API under /api/v2/service/
	// (#1283), which creates tenants and links users to the trusted issuer. It
	// is the bearer credential for that API: at least 32 characters, kept in a
	// Secret. Empty disables the API.
	ServiceToken string `mapstructure:"service_token"`
	// ExternalSignInURL hands unauthenticated UI visitors to the operator's own
	// sign-in instead of Dexaflow's page, with the requested path in a `next`
	// query parameter (#1288). The operator's flow is expected to return them
	// with a Dexaflow session. Empty keeps Dexaflow's page; `?local=1` reaches it
	// either way.
	ExternalSignInURL string `mapstructure:"external_signin_url"`
	// ExternalSignOutURL is where sign-out lands after clearing the session, so
	// the operator can end their own session too (#1288). Empty returns to
	// Dexaflow's sign-in page.
	ExternalSignOutURL string `mapstructure:"external_signout_url"`
	// DevNoAuth disables authentication entirely, treating every request as an
	// admin. It exists ONLY for `dexaflow lite` (local, unsandboxed). It is false by
	// default and the server logs a prominent warning when it is on. NEVER set
	// this in production (LEOFLOW_AUTH_DEV_NO_AUTH).
	DevNoAuth bool `mapstructure:"dev_no_auth"`
	// LoginRateLimitPerMinute caps failed /auth/token attempts per client IP per
	// minute (anti-brute-force). Only failures count, so a successful login never
	// consumes budget. Lite raises this well above the production default because
	// it is a local single-user tool where lockouts are pure friction.
	LoginRateLimitPerMinute int `mapstructure:"login_rate_limit_per_minute"`
	// SecretScoping is the operator scope-by-declaration policy (ADR 0055 D9):
	// "permissive" | "enforce" | "off". permissive (the default) delivers the
	// whole tenant vault and warns when a DAG declares a narrower set, an
	// empty set included (#800); enforce
	// delivers only the declared subset (empty declaration → nothing); off
	// disables scoping. It is operator-scoped, NEVER author-settable. Empty = the
	// permissive default.
	SecretScoping string `mapstructure:"secret_scoping"`
	// SecretLivenessMode gates secret delivery on task-instance liveness (ADR 0055
	// E2): "observe" | "enforce". observe (the default) logs + audits a
	// would-have-denied when the caller's TI is not live but still delivers;
	// enforce denies with PermissionDenied. Empty = the observe default.
	SecretLivenessMode string `mapstructure:"secret_liveness_mode"`
	// MaxAttemptCredentialLifetime is the hard ceiling on how long a single task
	// attempt's agent credential may be kept alive by heartbeat renewal (ADR 0055
	// Fix #4). Past this age since first dispatch, the control plane stops renewing
	// the token on heartbeat and lets it lapse, bounding a runaway attempt. The
	// short per-attempt TTL still bounds a stolen/finished token independently;
	// this caps the total renewed lifetime. It governs a second guarantee too: the
	// Kubernetes executor floors a task pod's activeDeadlineSeconds with it when
	// the DAG declares no execution_timeout, so a pod whose agent keeps retrying
	// its report through a total control-plane outage is not left Running
	// forever (a declared timeout is never shortened). Generous by default (24h)
	// so no normal task regresses. Bind via
	// LEOFLOW_AUTH_MAX_ATTEMPT_CREDENTIAL_LIFETIME as a duration (e.g. "24h",
	// "90m"). With warm pools enabled it is also the per-attempt watchdog that
	// keeps a wedged attempt from pinning a warm slot (a warm pod has no pod-level
	// deadline; the worker lifetime cap drains between attempts, never
	// mid-attempt). An attempt whose agent goes silent after running past the
	// ceiling is failed by the heartbeat reaper as a task failure with the
	// credential_ceiling reason (its retry policy applies), never re-placed as an
	// agent_lost infra loss with a fresh credential (#1461). In Lite, which has no
	// pod deadline, the reaper also fails an attempt still running past the
	// ceiling with that reason and stops its task (#1511). A non-positive value
	// disables the renewal ceiling, the pod deadline floor, that watchdog and the
	// credential_ceiling failure together (a wedged task then has no
	// wall-clock bound of its own), so boot logs a WARN naming the key.
	MaxAttemptCredentialLifetime time.Duration `mapstructure:"max_attempt_credential_lifetime"`
	// AgentTokenTransport selects how the in-pod agent obtains its control-plane
	// bearer credential (ADR 0055 Fix #3): "envvar" (the default) sets the token as
	// a plaintext LEOFLOW_AGENT_TOKEN env var on the pod spec, today's behavior,
	// byte-identical; "exchange" mounts a projected ServiceAccount token that the
	// agent exchanges once (via a control-plane TokenReview) for the task-scoped
	// JWT, so no bearer sits in plaintext on the Pod object. The exchange path is
	// Pro/Kubernetes-executor-only (the subprocess executor has no pod/SA/TokenReview
	// and ignores this). It is operator-scoped, NEVER author-settable. Empty = the
	// envvar default. Bind via LEOFLOW_AUTH_AGENT_TOKEN_TRANSPORT.
	AgentTokenTransport string `mapstructure:"agent_token_transport"`
	// SessionCookieInsecure drops the Secure attribute from the browser session
	// cookie (_token) and the OIDC state cookie. It is operator-scoped, NEVER
	// author-settable, and defaults to false, which is the hardened posture.
	//
	// There is exactly one reason to set it: a deployment served over plain http
	// to something that is not a loopback address. A browser refuses a Secure
	// cookie from such an origin outright, so the login page would post valid
	// credentials, get a 200, and land back on itself with no error anywhere. A
	// loopback deployment (localhost, 127.0.0.1) needs nothing: browsers treat it
	// as trustworthy and accept the Secure cookie over http.
	//
	// It cannot be derived from the request. Behind a TLS-terminating ingress the
	// server sees plain http while the browser sees https, so request-derived
	// Secure would strip it from the deployment that most needs it. Boot logs a
	// WARN when it is on. Bind via LEOFLOW_AUTH_SESSION_COOKIE_INSECURE.
	SessionCookieInsecure bool `mapstructure:"session_cookie_insecure"`
	// SessionCookieName is the name of the browser session cookie (#1405).
	// The default, _token, is what every earlier release used, so an existing
	// install keeps its sessions. A name starting with __Host- makes the
	// browser refuse the cookie unless it is Secure, has Path=/ and no Domain,
	// which is how the session cookie is always set: a page on a sibling host
	// under the same parent domain can then no longer plant a session here.
	// Recommended for any deployment that serves several hosts under one
	// parent domain. A __Host- or __Secure- name with SessionCookieInsecure
	// fails boot, because the browser would drop every session cookie.
	// Empty means _token. Bind via LEOFLOW_AUTH_SESSION_COOKIE_NAME.
	SessionCookieName string `mapstructure:"session_cookie_name"`
}

// APITokensSection configures the API tokens of ADR 0074 (#2056).
type APITokensSection struct {
	// Disabled stops serving the API tokens: /api/v2/apiTokens and the
	// tokens screen are not registered, and every API token is refused as
	// an unknown bearer, a token minted while they were served included (it
	// works again if they are served again before it expires). For a
	// platform in front of the engine that decides who may reach it at each
	// sign-in, so no engine credential outlives that decision. False, the
	// zero value, serves them, so a configuration that never mentions it
	// keeps them.
	Disabled bool `mapstructure:"disabled"`
}

// JWTSection configures JWT issuance and validation.
type JWTSection struct {
	Secret          string `mapstructure:"secret"`
	TokenTTLSeconds int    `mapstructure:"token_ttl_seconds"`
	// MaxLifetimeSeconds is the hard ceiling on how long a user session may be kept
	// alive by transparent token renewal (aresta #5), measured since first login
	// (the token's oiat claim). Past it, POST /api/v2/auth/token/renew is refused
	// and the user must `dexaflow auth login` again. The short TokenTTLSeconds still
	// bounds a stolen token independently; this only caps the total renewed
	// lifetime, mirroring auth.max_attempt_credential_lifetime for agent tokens.
	// Generous by default (24h) so a normal dev day never re-logs in mid-session; a
	// non-positive value disables the ceiling (renewal never expires the session).
	// Bind via LEOFLOW_AUTH_JWT_MAX_LIFETIME_SECONDS.
	MaxLifetimeSeconds int `mapstructure:"max_lifetime_seconds"`
}

// OIDCSection configures the OIDC/SSO login flow (Authorization Code + PKCE).
// It is read only when auth.provider is "oidc", which is Pro-gated and fails
// boot closed unless Issuer, ClientID, and RedirectURL are all set.
//
// Verification is keyless: the ID token is validated against the issuer's
// public JWKS discovered from Issuer, so no secret is stored for the verify
// path. ClientSecret is used solely for the authorization-code exchange and is
// injected via LEOFLOW_AUTH_OIDC_CLIENT_SECRET (env, never persisted, never
// logged), the same posture as the JWT secret.
type OIDCSection struct {
	// Issuer is the org's single-tenant issuer URL (https). It is pinned: any ID
	// token whose iss claim differs is rejected (fail-closed tenant pin).
	Issuer string `mapstructure:"issuer"`
	// ClientID is the registered application (client) id; it is the expected
	// audience of every ID token.
	ClientID string `mapstructure:"client_id"`
	// ClientSecret is used only for the code exchange. Set via
	// LEOFLOW_AUTH_OIDC_CLIENT_SECRET; never persist it in a config file.
	ClientSecret string `mapstructure:"client_secret"`
	// RedirectURL is this server's callback URL registered with the IdP
	// (…/api/v2/auth/oidc/callback).
	RedirectURL string `mapstructure:"redirect_url"`
	// Scopes are the OAuth scopes requested; defaults to openid, email, profile.
	// Add the IdP's groups scope here when group→role mapping is used.
	Scopes []string `mapstructure:"scopes"`
	// GroupsClaim is the ID-token claim carrying the user's IdP groups (default
	// "groups"). Its values drive RoleMappings.
	GroupsClaim string `mapstructure:"groups_claim"`
	// RoleMappings maps an IdP group value to an existing Dexaflow role name.
	// Default-DENY: a group with no mapping grants no role. Configure via a YAML
	// config file only. The chart ships none today, so this map has no route
	// through Helm (#1143).
	//
	// Decoded OUT-OF-BAND (mapstructure:"-"), not by viper: viper's "." key
	// delimiter splits a dotted MAP KEY (a dotted IdP group like "app.admins")
	// into nested maps and fails to decode. LoadServer parses this map straight
	// from the raw YAML instead (#826). Env-var binding never applied to maps.
	RoleMappings map[string]string `mapstructure:"-" yaml:"role_mappings"`
	// DefaultRole softens the default-deny WITHOUT weakening the secure default:
	// when an authenticated user resolves to zero mapped roles and DefaultRole is
	// set, they are granted this single role (operators are advised to use a
	// read-only role such as "viewer"). Empty (the default) keeps strict
	// default-deny, an unmapped user gets no role. It must name an existing DB
	// role for the resolved tenant; an unknown role fails the login closed.
	DefaultRole string `mapstructure:"default_role"`
	// TenantClaim selects which IdP claim identifies the tenant: "tid" (Entra) or
	// "hd" (Google Workspace).
	TenantClaim string `mapstructure:"tenant_claim"`
	// TenantClaims maps a TenantClaim value to a Dexaflow tenant name. A value not
	// present here is rejected (403), the login never falls back to "default".
	//
	// Decoded OUT-OF-BAND (mapstructure:"-"), not by viper: a Google Workspace
	// `hd` value is a domain (always dotted, e.g. "example.com" or
	// "sub.example.co.uk"), which viper's "." delimiter would split into nested
	// maps and fail to decode, the #826 crash. LoadServer parses this map from
	// the raw YAML instead.
	TenantClaims map[string]string `mapstructure:"-" yaml:"tenant_claims"`
	// AllowedEmailDomains is an install-time, login-level allowlist layered on TOP
	// of the tid/hd tenant pin, it is NOT the pin itself (that stays issuer +
	// tid/hd + email_verified per D6). The check runs only AFTER the pin and
	// email_verified==true have passed, so the email domain is trustworthy at that
	// point. Empty (the default) imposes no domain restriction, the tid/hd pin is
	// the sole boundary. Non-empty admits a login (pre-provisioned OR JIT) only
	// when the verified email's domain is in the list; every other login is
	// rejected 403. It gates EVERY OIDC login, not just auto-provisioning.
	AllowedEmailDomains []string `mapstructure:"allowed_email_domains"`
	// BreakGlassEmails is the allowlist of local password logins permitted while
	// provider is "oidc"; every other password login is rejected (SSO-only).
	BreakGlassEmails []string `mapstructure:"break_glass_emails"`
	// JITProvisioning creates a user row on first OIDC login when no matching one
	// exists; the new row is granted the roles from RoleMappings. OFF by default.
	//
	// OFF denies every first login. A login matches a user only by
	// (oidc_provider, oidc_subject) and CreateOIDCUser, reached only from this
	// path, is the sole statement that writes those columns, so no API, CLI or
	// migration can pre-create an OIDC identity (ADR 0057, amendment on D4).
	// cmd/dexaflow-server warns about this at boot.
	JITProvisioning bool `mapstructure:"jit_provisioning"`
	// AutoRedirect starts the login flow on the sign-in page instead of rendering
	// it, for a deployment where that page is a screen to acknowledge for nothing
	// (an edge proxy has already authenticated, or SSO is the only way in).
	//
	// OFF by default: turning it on for everyone would remove the sign-in page
	// from deployments that rely on it. It is suppressed on a refused sign-on and
	// behind an explicit ?local=1, so a denial still lands somewhere readable and
	// a break-glass account can always reach the form.
	AutoRedirect bool `mapstructure:"auto_redirect"`
	// ClockSkewSeconds is the tolerance applied to the ID token's exp/iat/nbf
	// checks to absorb small clock differences between the IdP and this server.
	// Defaults to 60.
	ClockSkewSeconds int `mapstructure:"clock_skew_seconds"`
}

// Auth providers (auth.provider allowlist). "jwt" is the default credential
// authenticator; "oidc" adds the SSO login flow on top of it (the JWT
// authenticator stays the request-path verifier in both modes).
const (
	// AuthProviderJWT is the default: username/password issues an HS256 token.
	AuthProviderJWT = "jwt"
	// AuthProviderOIDC enables the OIDC/SSO login flow. It is Pro-gated and
	// requires the auth.oidc.* configuration; boot fails closed otherwise.
	AuthProviderOIDC = "oidc"
)

// validateProvider rejects an unknown auth.provider, failing closed at boot
// instead of letting main.go build an authenticator regardless of what was
// configured. Empty is valid: serverDefaults sets auth.provider to "jwt", so an
// unset provider in an existing config keeps defaulting to JWT and is
// unaffected. "oidc" is valid only when its Pro-gated prerequisites are met
// (validateOIDC); anything else is a loud boot failure.
func (c *ServerConfig) validateProvider() error {
	switch c.Auth.Provider {
	case "", AuthProviderJWT:
		return nil
	case AuthProviderOIDC:
		return c.validateOIDC()
	default:
		return fmt.Errorf("invalid auth.provider %q: must be %q or %q (or empty = %q)",
			c.Auth.Provider, AuthProviderJWT, AuthProviderOIDC, AuthProviderJWT)
	}
}

// validateOIDC enforces the fail-closed prerequisites for auth.provider: oidc
// (D7): the Pro edition, and the fields the login flow cannot run without
// (issuer, client_id, redirect_url, and the tenant pin). The issuer must be
// https so discovery and JWKS are fetched over TLS (keyless verify, ADR 0035).
// A misconfigured OIDC deployment fails boot with an actionable message rather
// than starting a login flow that cannot complete.
//
// The tenant pin belongs in this set because it decides whether ANY login can
// succeed: Verify resolves a tenant on every login and fails closed when
// tenant_claim is unset or the claim value is not in tenant_claims
// (internal/oidc/verify.go). That rejection is correct, but it reaches the user
// as a generic 403 and its cause reaches only the audit log, which in an
// SSO-only deployment nobody can log in to read (#1143).
//
// Every missing key is reported in one error on purpose. Each boot failure on a
// Kubernetes deployment costs a values edit, an upgrade and a rollout to learn
// the next one, so reporting them one at a time turns first-time SSO setup into
// a chain of CrashLoopBackOffs.
func (c *ServerConfig) validateOIDC() error {
	if c.UI.Edition != "pro" {
		return errors.New("auth.provider: oidc requires the Pro edition (set ui.edition: pro)")
	}
	var missing []string
	if c.Auth.OIDC.Issuer == "" {
		missing = append(missing, "auth.oidc.issuer")
	}
	if c.Auth.OIDC.ClientID == "" {
		missing = append(missing, "auth.oidc.client_id")
	}
	if c.Auth.OIDC.RedirectURL == "" {
		missing = append(missing, "auth.oidc.redirect_url")
	}
	if c.Auth.OIDC.TenantClaim == "" {
		missing = append(missing, "auth.oidc.tenant_claim")
	}
	if len(c.Auth.OIDC.TenantClaims) == 0 {
		missing = append(missing, "auth.oidc.tenant_claims")
	}
	if len(missing) > 0 {
		return fmt.Errorf("auth.provider: oidc requires %s to be set%s", strings.Join(missing, ", "), tenantPinHint(c))
	}
	if err := validateOIDCNames(c.Auth.OIDC); err != nil {
		return err
	}
	if !strings.HasPrefix(c.Auth.OIDC.Issuer, "https://") {
		return fmt.Errorf("auth.oidc.issuer must be an https:// URL (got %q)", c.Auth.OIDC.Issuer)
	}
	return validateRedirectURL(c.Auth.OIDC.RedirectURL)
}

// validateOIDCNames rejects a name that is present but blank.
//
// "corp.example:" with nothing after it is valid YAML binding to the empty
// string, and the checks above only ask whether the map is non-empty. A blank
// tenant name resolves a login to a tenant that cannot exist. A blank role name
// is copied straight into the resolved role set by oidc.MapRoles and then fails
// the existence check. Both deny the login behind the same generic answer as
// every other failure, and the operator sees a key they did fill in.
//
// This fails boot where its neighbors only warn, and the difference is that it
// cannot be a transient. The tenant and role existence checks ask a live
// database, where "absent" and "could not ask" are the same answer during a
// blip, so a hard gate there turns a lagging replica into a restart loop. This
// is a string in a file: a blank name is never correct, no deployment can be
// working with one, and the answer is identical on every boot.
//
// Keys are checked too. A claim value or an IdP group that is blank can never be
// what an IdP sends, so the entry is unreachable rather than wrong, which is the
// same defect wearing the other hat.
func validateOIDCNames(o OIDCSection) error {
	for _, m := range []struct {
		key     string
		entries map[string]string
		keyWhat string
		valWhat string
	}{
		{"auth.oidc.tenant_claims", o.TenantClaims, "claim value", "tenant name"},
		{"auth.oidc.role_mappings", o.RoleMappings, "IdP group", "role name"},
	} {
		for k, v := range m.entries {
			if strings.TrimSpace(k) == "" {
				return fmt.Errorf("%s has an entry whose %s is blank; nothing an IdP sends can match it, so the entry is unreachable", m.key, m.keyWhat)
			}
			if strings.TrimSpace(v) == "" {
				return fmt.Errorf("%s maps %q to a blank %s; every login it governs is denied, because no %s resolves. Give it a value, or remove the entry",
					m.key, k, m.valWhat, m.valWhat)
			}
		}
	}
	// An ABSENT default_role is the documented strict posture. A blank one is a
	// typo that reads as set and grants nothing.
	if o.DefaultRole != "" && strings.TrimSpace(o.DefaultRole) == "" {
		return errors.New("auth.oidc.default_role is set to whitespace; leave it unset for strict default-deny, or name a role that exists")
	}
	return nil
}

// tenantPinHint explains the tenant pin when one of its two keys is missing, and
// returns "" otherwise. It is the whole remedy in one line, because this error is
// printed before the logger exists and is the only thing a crash-looping
// container leaves behind: what breaks without the pin, what to set, the single
// route that can carry the map (env vars cannot), and how to restore password
// login while the SSO configuration is being worked out.
func tenantPinHint(c *ServerConfig) string {
	if c.Auth.OIDC.TenantClaim != "" && len(c.Auth.OIDC.TenantClaims) > 0 {
		return ""
	}
	return ". The tenant pin decides whether any login can succeed: a claim value that is absent or not mapped is rejected with 403 (audited as tenant_not_allowed) and never falls back to the default tenant, so without it every SSO login fails. " +
		"The pin is two settings: auth.oidc.tenant_claim names the claim carrying the tenant (tid on Entra, hd on Google Workspace), and auth.oidc.tenant_claims maps each value of it you accept to a Dexaflow tenant. " +
		"auth.oidc.tenant_claims is a map, so it loads ONLY from the YAML config file named by DEXAFLOW_CONFIG; no DEXAFLOW_AUTH_OIDC_* environment variable can carry it. " +
		"To keep serving password logins while SSO is configured, set auth.provider: jwt"
}

// validatePlatformIntegration checks the settings an operator uses to serve
// Dexaflow from inside a larger platform: external sign-in and sign-out (#1288),
// the trusted issuer (#1284), the service API token (#1283), the home link
// (#1290) and branding (#1289).
func (c *ServerConfig) validatePlatformIntegration() error {
	if err := validateExternalAuthURL("auth.external_signin_url", c.Auth.ExternalSignInURL); err != nil {
		return err
	}
	if err := validateExternalAuthURL("auth.external_signout_url", c.Auth.ExternalSignOutURL); err != nil {
		return err
	}
	if err := validateTrustedIssuer(c.Auth.TrustedIssuer); err != nil {
		return err
	}
	if err := validateSessionCookieName(c.Auth.SessionCookieName, c.Auth.SessionCookieInsecure); err != nil {
		return err
	}
	if t := c.Auth.ServiceToken; t != "" && len(strings.TrimSpace(t)) < 32 {
		return errors.New("auth.service_token must be at least 32 characters (generate one with `openssl rand -base64 48`)")
	}
	if err := validateHomeLink(c.UI.HomeLink); err != nil {
		return err
	}
	return validateBranding(c.UI)
}

// validateExternalAuthURL checks one of the #1288 settings: empty, or an
// absolute http(s) URL with a host. A relative URL would send the browser back
// into Dexaflow, where the sign-in route redirects again: a loop.
func validateExternalAuthURL(key, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s must be a valid URL (got %q): %w", key, raw, err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%s must be an absolute http:// or https:// URL (got %q)", key, raw)
	}
	return nil
}

// DefaultSessionCookieName is the browser session cookie's name when
// auth.session_cookie_name is unset: the name every earlier release used.
const DefaultSessionCookieName = "_token"

// oidcStateCookieName is the OIDC flow's own cookie. The session cookie may
// not take its name, or each login would overwrite the other.
const oidcStateCookieName = "_oidc_state"

// sessionCookieNamePattern is an RFC 6265 cookie name: one or more token
// characters, so no space, control character or separator such as ; or =.
var sessionCookieNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

// validateSessionCookieName checks auth.session_cookie_name (#1405). A browser
// drops a __Host- or __Secure- cookie that is not Secure, so with
// auth.session_cookie_insecure on such a name would fail every sign-in with no
// visible error. Browsers match those prefixes in any case, so this does too.
func validateSessionCookieName(name string, insecure bool) error {
	if name == "" {
		return nil
	}
	if !sessionCookieNamePattern.MatchString(name) || name == oidcStateCookieName {
		return fmt.Errorf("auth.session_cookie_name must be a cookie name (letters, digits and !#$%%&'*+-.^_`|~, not %s) (got %q)", oidcStateCookieName, name)
	}
	lower := strings.ToLower(name)
	if insecure && (strings.HasPrefix(lower, "__host-") || strings.HasPrefix(lower, "__secure-")) {
		return fmt.Errorf("auth.session_cookie_name %q needs the Secure attribute, which auth.session_cookie_insecure removes; "+
			"a browser drops such a cookie, so nobody could sign in. Serve the deployment over https, or use a name without the __Host- or __Secure- prefix", name)
	}
	return nil
}

// issuerNamePattern is the shape of a trusted issuer's name: it becomes part of
// the provider key stored on every linked user.
var issuerNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// validateTrustedIssuer checks auth.trusted_issuer (#1284) when any key is set.
// Every missing or malformed key is reported in one error, so first-time setup
// is one edit rather than a chain of restarts.
func validateTrustedIssuer(s TrustedIssuerSection) error {
	if !s.Enabled() && s.Name == "" && s.JWKSURL == "" && s.Audience == "" && len(s.AllowedTenants) == 0 && len(s.AllowedOrigins) == 0 && s.HostPattern == "" && s.DeployAudience == "" {
		return nil
	}
	checks := []struct {
		ok      bool
		problem string
	}{
		{issuerNamePattern.MatchString(s.Name), "auth.trusted_issuer.name (1-40 lowercase letters, digits or '-')"},
		{s.Issuer != "", "auth.trusted_issuer.issuer"},
		{jwksURLAllowed(s.JWKSURL), "auth.trusted_issuer.jwks_url (https, or http on a loopback host)"},
		{s.Audience != "", "auth.trusted_issuer.audience"},
		{s.TenantClaim != "", "auth.trusted_issuer.tenant_claim"},
		{len(s.AllowedTenants) > 0, `auth.trusted_issuer.allowed_tenants (tenant names, or "*" for all)`},
		{s.MaxLifetimeSeconds >= 0 && s.MaxLifetimeSeconds <= 600, "auth.trusted_issuer.max_lifetime_seconds (0 to 600)"},
		{originsValid(s.AllowedOrigins), "auth.trusted_issuer.allowed_origins (one or more scheme://host[:port], no path)"},
		{bearerAudiencesValid(s), "auth.trusted_issuer.bearer_audiences (non-empty, and none equal to auth.trusted_issuer.audience)"},
		{s.BearerMaxLifetimeSeconds >= 0 && s.BearerMaxLifetimeSeconds <= 3600, "auth.trusted_issuer.bearer_max_lifetime_seconds (0 to 3600)"},
		{hostPatternValid(s.HostPattern), "auth.trusted_issuer.host_pattern (empty, or a host[:port] with exactly one {tenant}, such as {tenant}.example.com)"},
		{deployAudienceValid(s), "auth.trusted_issuer.deploy_audience (empty, or an audience without surrounding spaces other than auth.trusted_issuer.audience and every auth.trusted_issuer.bearer_audiences entry)"},
	}
	var problems []string
	for _, c := range checks {
		if !c.ok {
			problems = append(problems, c.problem)
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("auth.trusted_issuer is incomplete or invalid; set: %s", strings.Join(problems, ", "))
	}
	return nil
}

// bearerAudiencesValid reports whether every bearer audience is set and none
// is the handoff audience: a token for both would be a one-use handoff and a
// reusable bearer at once.
func bearerAudiencesValid(s TrustedIssuerSection) bool {
	for _, aud := range s.BearerAudiences {
		if aud == "" || aud == s.Audience {
			return false
		}
	}
	return true
}

// deployAudienceValid reports whether the deploy audience is unset, or free
// of surrounding whitespace (aud is compared exactly, so " x" would match no
// token and read as a typo) and shared with no other audience: a handoff or
// another client's bearer that is also a deploy token would undo the
// restriction it exists for (#1773).
func deployAudienceValid(s TrustedIssuerSection) bool {
	d := s.DeployAudience
	if d == "" {
		return true
	}
	return d == strings.TrimSpace(d) && d != s.Audience && !slices.Contains(s.BearerAudiences, d)
}

// hostPatternChars is what a host pattern may hold once {tenant} is taken
// out: a host name and an optional port. No scheme, path or wildcard, so the
// pattern names exactly one host per tenant.
var hostPatternChars = regexp.MustCompile(`^[A-Za-z0-9.-]*(:\d{1,5})?$`)

// hostPatternValid accepts an empty pattern (no binding), or a host[:port]
// holding exactly one {tenant}.
func hostPatternValid(p string) bool {
	if p == "" {
		return true
	}
	if strings.Count(p, HostPatternTenant) != 1 {
		return false
	}
	return hostPatternChars.MatchString(strings.Replace(p, HostPatternTenant, "t", 1))
}

// jwksURLAllowed accepts an https URL with a host, or http on a loopback host
// for local development, like the OIDC redirect URL.
func jwksURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname()))
}

// validateRedirectURL requires the OIDC callback URL to use https so the
// authorization code is never returned over plaintext, with an http exception
// for loopback hosts (localhost, 127.0.0.1, [::1]) to keep local dev workable.
func validateRedirectURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("auth.oidc.redirect_url must be a valid URL (got %q): %w", raw, err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("auth.oidc.redirect_url must be an https:// URL (got %q); http:// is only allowed for loopback hosts (localhost, 127.0.0.1, [::1])", raw)
	default:
		return fmt.Errorf("auth.oidc.redirect_url must be an https:// URL (got %q)", raw)
	}
}
