package main

import (
	"context"
	"fmt"
	"iter"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/observability"
)

// validateStartup runs every boot-time invariant check before any subsystem
// starts: the operator's configuration, then the resilience timing ladder the
// restart recovery depends on. A violation fails boot with the offending
// relation named, instead of surfacing as a false reap after the next restart.
func validateStartup(cfg *config.ServerConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	return executor.ValidateResilienceLadder(resilienceLadder(cfg))
}

// warnStartup surfaces the boot-time settings validateStartup accepts but that
// remove a resilience backstop, today the disabled credential ceiling, which
// unbounds heartbeat renewal, drops the task pod deadline floor and switches
// off the warm-pool attempt watchdog. They are documented settings, not errors,
// so boot proceeds; the WARN is the only operator-visible signal that those
// guarantees are off. It runs after the logger exists, right after validation
// has passed. Each warning is logged with the config key and the offending
// value as attributes, so the JSON record an alert rule reads carries
// config_key/value fields instead of only a prose message (#924).
// Only a process that serves the scheduler warns: token renewal
// lives in the agent gRPC server and the pod deadline floor in the dispatcher,
// both scheduler-side, so an api-only replica in a split install would be
// warning about behavior it does not implement, and a WARN operators learn to
// ignore is worse than none.
func warnStartup(cfg *config.ServerConfig, logger *slog.Logger) {
	// Role resolution is API-side, so this one is gated on the API rather than the
	// scheduler: an api-only replica is the process that reconciles roles.
	if cfg.Server.ServesAPI() {
		// Both are API-side: role reconciliation and the code exchange both run in
		// the process that serves the callback.
		oidcWarnings := oidcRoleSourceWarnings(cfg.Auth)
		oidcWarnings = append(oidcWarnings, oidcClientSecretWarnings(cfg.Auth)...)
		oidcWarnings = append(oidcWarnings, oidcJITWarnings(cfg.Auth)...)
		oidcWarnings = append(oidcWarnings, oidcBreakGlassWarnings(cfg.Auth)...)
		// Not OIDC-specific, but API-side for the same reason: the process that
		// serves the sign-in page and the callback is the one that sets the cookie.
		oidcWarnings = append(oidcWarnings, sessionCookieWarnings(cfg.Auth)...)
		for _, w := range oidcWarnings {
			logger.Warn(w.Msg, "config_key", w.Key, "value", w.Value, "missing_config_key", w.MissingKey)
		}
	}
	if !cfg.Server.ServesScheduler() {
		return
	}
	for _, w := range executor.ResilienceLadderWarnings(resilienceLadder(cfg)) {
		logger.Warn(w.Msg, "event", w.Event, "config_key", w.Key, "value", w.Value)
	}
	for _, w := range platformDefaultWarnings(cfg.Executor.Defaults) {
		logger.Warn(w.Msg, "config_key", w.Key, "value", w.Value, "missing_config_key", w.MissingKey)
	}
	for _, w := range logPersistenceWarnings(cfg.Logs) {
		logger.Warn(w.Msg, "config_key", w.Key, "value", w.Value)
	}
}

// recordResiliencePosture exports the boot posture the ladder WARN describes as
// dexaflow_resilience_backstop_disabled, one series per backstop key. A
// disabled backstop stays disabled for the life of the process, while its log
// record scrolls out of retention, so "is any replica running with a backstop
// off" needs a gauge, not a log search (#941). Gated like the WARN: only a
// process that runs the scheduler implements the backstops.
func recordResiliencePosture(cfg *config.ServerConfig, m *observability.Metrics) {
	if !cfg.Server.ServesScheduler() {
		return
	}
	disabled := map[string]bool{}
	for _, w := range executor.ResilienceLadderWarnings(resilienceLadder(cfg)) {
		if w.Event == executor.EventResilienceBackstopDisabled {
			disabled[w.Key] = true
		}
	}
	for _, key := range executor.ResilienceBackstopKeys() {
		m.RecordResilienceBackstop(key, disabled[key])
	}
}

// logPersistenceWarnings reports task logs configured on an ephemeral disk volume.
func logPersistenceWarnings(c config.LogsSection) []configWarning {
	const key = "logs.persistence_enabled"
	if c.Backend == "disk" && !c.PersistenceEnabled {
		return []configWarning{{
			Msg:   key + " is false with backend disk: task logs are stored on ephemeral storage and will not survive a control plane restart",
			Key:   key,
			Value: "false",
		}}
	}
	return nil
}

// configWarning is one boot WARN about an operator setting that is accepted but
// quietly delivers less than the value is documented to deliver: the
// operator-readable sentence plus the fields a monitoring rule needs. Same shape
// and same reason as executor.LadderWarning, a pre-formatted string alone
// leaves the JSON record with nothing but msg, so the alert an operator would
// actually want can only be written as a substring match on prose that a later
// reword silently breaks. Msg stays self-contained so a plain-text log needs no
// field expansion to be read.
type configWarning struct {
	Msg   string
	Key   string
	Value string
	// MissingKey is the sibling key the operator has to set for the documented
	// behavior to hold.
	MissingKey string
}

// platformDefaultWarnings reports executor.defaults settings that pass
// validation but silently deliver less than the value promises. Today there is
// one: a PARTIAL resources pair.
//
// resources_cpu/resources_memory are documented, in the chart's values.yaml, in
// the rendered chart README, and in the configuration reference, as landing a
// task that declares no resources of its own in Guaranteed QoS. That holds only
// when BOTH are set, because Guaranteed requires requests == limits for cpu AND
// memory. With one set, platformDefaults (policies.go) still builds a non-nil default,
// the empty dimension is dropped from the pod spec, and the task gets one
// dimension pinned and the other with no request or limit at all: Burstable,
// first throttled/evicted under node pressure, and invisible to the Cluster
// Autoscaler's scale-up math on the dimension left out. A half-configured
// default is better than none, so it is not a boot error, but it is otherwise
// invisible, and the operator who set one field believes they configured the
// documented behavior. Pure, like the ladder warnings: called once at boot,
// after the logger exists.
func platformDefaultWarnings(c config.PlatformDefaultsSection) []configWarning {
	const cpuKey, memKey = "executor.defaults.resources_cpu", "executor.defaults.resources_memory"
	if (c.ResourcesCPU == "") == (c.ResourcesMemory == "") {
		// Both set (the documented configuration) or neither (the shipped
		// default). Nothing to say.
		return nil
	}
	set, missing, value, dimension := cpuKey, memKey, c.ResourcesCPU, "memory"
	if c.ResourcesCPU == "" {
		set, missing, value, dimension = memKey, cpuKey, c.ResourcesMemory, "cpu"
	}
	return []configWarning{{
		Msg: fmt.Sprintf("%s is set (%q) with no %s: a task that declares no resources of its own gets that one quantity as its request AND limit and NO %s request or limit at all, so the pod is Burstable, not the Guaranteed QoS this default is documented to deliver, and nothing else supplies the missing %s. Set both quantities, or neither",
			set, value, missing, dimension, dimension),
		Key:        set,
		Value:      value,
		MissingKey: missing,
	}}
}

// defaultRoleKey is the config key that gives OIDC role resolution a floor. Two
// boot warnings point at it, so it is named once.
const defaultRoleKey = "auth.oidc.default_role"

// oidcRoleSourceWarnings reports an OIDC deployment whose role resolution has no
// floor: auth.oidc.default_role is empty, with or without auth.oidc.role_mappings.
// The two shapes get different messages because they are different mistakes.
//
// Role resolution is IdP-authoritative (ADR 0057 D5): every login computes its
// roles from role_mappings, applies default_role when that yields none, and hands
// the result to ReconcileUserRoles, which sets the user's DB grants to EXACTLY
// that set. With neither configured the set is always empty, and an empty
// reconcile is a full clear, so a pre-provisioned admin's first SSO login strips
// the grants they were provisioned with. The login still succeeds and the session
// is minted, which is why this cannot fail closed: nothing is wrong per request,
// the deployment is simply configured to grant nobody anything.
//
// It only became reachable when the tenant pin was made a boot requirement
// (#1143): before that, a deployment configured this loosely was usually also
// missing the pin, and the login was rejected before reconciliation ran. Removing
// that accidental shield is what makes the WARN necessary.
//
// With role_mappings set the same clear happens per login rather than always, and
// boot cannot tell a broken deployment from a strict one: on an IdP that emits
// the groups claim, granting nothing outside a mapped group is the intended
// posture, while on Google Workspace without Directory API group sync the claim
// never arrives and the mapping can never match. That is why this half names the
// Google shape and ends with a clause the operator can deliberately dismiss:
// default_role is not a free fix, it grants that role to every login the tenant
// pin admits, and a warning whose only remedy is to widen access is one a correct
// deployment learns to ignore.
func oidcRoleSourceWarnings(c config.AuthSection) []configWarning {
	const mappingsKey = "auth.oidc.role_mappings"
	if c.Provider != config.AuthProviderOIDC {
		return nil
	}
	if c.OIDC.DefaultRole != "" {
		return nil
	}
	if len(c.OIDC.RoleMappings) > 0 {
		return []configWarning{{
			Msg: "auth.oidc.role_mappings without " + defaultRoleKey +
				" makes the IdP group claim load-bearing: a login whose groups match no entry resolves to an EMPTY role set, " +
				"and roles are reconciled to exactly that set, so the user's existing grants are CLEARED rather than left alone. " +
				"Google Workspace emits no groups claim at all unless Directory API group sync is configured, " +
				"so every login on that IdP takes this path. Set " + defaultRoleKey +
				" to a read-only role such as viewer to give resolution a floor, or ignore this if your IdP does emit " +
				"auth.oidc.groups_claim and a login outside every mapped group is meant to hold no roles",
			Key:        defaultRoleKey,
			Value:      "",
			MissingKey: defaultRoleKey,
		}}
	}
	return []configWarning{{
		Msg: "auth.provider: oidc with neither " + mappingsKey + " nor " + defaultRoleKey +
			" grants no roles to anyone: every login resolves to an empty role set, and roles are reconciled to EXACTLY that set on each login, " +
			"so a pre-provisioned user's existing grants are CLEARED on their first single sign-on. " +
			"Map at least one IdP group to a role under " + mappingsKey + " (config file only: it is a map), or set " + defaultRoleKey +
			" to a read-only role such as viewer",
		Key:        defaultRoleKey,
		Value:      "",
		MissingKey: mappingsKey,
	}}
}

// warnOIDCNames runs the database-backed name check and logs what it finds.
//
// Gated on the API role for the same reason the role warning is: the login path
// is API-side, so this is the process whose logins the missing names break. The
// lookups are bounded because this is on the boot path ahead of the listener,
// and a hung query must not hold the probe endpoint down.
func warnOIDCNames(ctx context.Context, ck oidcNameChecker, cfg *config.ServerConfig, logger *slog.Logger) {
	if !cfg.Server.ServesAPI() {
		return
	}
	nameCtx, cancel := context.WithTimeout(ctx, oidcNameCheckTimeout)
	defer cancel()
	for _, w := range oidcNameWarnings(nameCtx, ck, cfg.Auth) {
		logger.Warn(w.Msg, "config_key", w.Key, "value", w.Value, "missing_config_key", w.MissingKey)
	}
}

// oidcNameChecker is the pair of existence lookups the boot name check needs.
// It is an interface so the check is testable without a database, and narrow so
// it cannot grow into a second place that reads users.
type oidcNameChecker interface {
	TenantExists(ctx context.Context, name string) (bool, error)
	RoleExists(ctx context.Context, tenant, role string) (bool, error)
	LocalPasswordUserExists(ctx context.Context, tenant, email string) (bool, error)
}

// oidcNameWarnings reports OIDC settings that name rows which do not exist.
//
// Everything else in this file checks the shape of the configuration. These two
// are the shapes that are correct and still cannot work, because the name is
// fine and the row is missing:
//
//   - a tenant_claims VALUE naming a tenant that does not exist. The
//     migrations create only "default" (INSERT INTO tenants appears once, in
//     001); any other tenant exists only once an operator creates it, through
//     the service API (PUT /api/v2/service/tenants/{name}, #1283). Until then
//     every login carrying that claim value is denied.
//   - a default_role or role_mappings value naming a role that does not exist
//     for the resolved tenant. The ladder (viewer, editor, operator, admin) is
//     seeded for "default" alone. This one matters because "set default_role to
//     viewer" is the remedy this binary's own warnings recommend, so it is the
//     name most likely to be typed by hand.
//
// It runs after the schema check, so Postgres is connected and current. A lookup
// that fails is reported rather than skipped: a check that could not run and a
// check that found nothing must not produce the same silence.
//
// Both maps are walked per distinct VALUE rather than per key (keysByValue),
// because the value is the name being looked up: several domains commonly map to
// one tenant and several IdP groups to one role, and the same missing row asked
// for once per key is a repeated round trip on the boot path and a warning
// repeated until operators stop reading it. The keys that named it travel with
// it, so one warning still points at every line that has to be edited.
func oidcNameWarnings(ctx context.Context, ck oidcNameChecker, c config.AuthSection) []configWarning {
	if c.Provider != config.AuthProviderOIDC {
		return nil
	}
	const tenantsKey = "auth.oidc.tenant_claims"
	out := breakGlassAddressWarnings(ctx, ck, c.OIDC.BreakGlassEmails)
	// Walked per distinct TENANT, not per claim value: several domains commonly
	// map to the same tenant, and asking the same question once per domain is a
	// repeated round trip on the boot path and a warning repeated until operators
	// stop reading it. The claim values that named the tenant travel with it, so
	// one warning still points at every line that has to be edited.
	for tenant, claims := range keysByValue(c.OIDC.TenantClaims) {
		ok, err := ck.TenantExists(ctx, tenant)
		switch {
		case err != nil:
			out = append(out, configWarning{
				Msg: "could not check whether the tenant " + quoted(tenant) + " named by " + tenantsKey +
					" exists: " + err.Error() + ". A login resolving to a tenant that does not exist is denied, " +
					"so this is worth re-running by hand",
				Key:   tenantsKey,
				Value: tenant,
			})
			continue
		case !ok:
			out = append(out, configWarning{
				Msg: tenantsKey + " maps " + quotedList(claims) + " to the tenant " + quoted(tenant) +
					", which does not exist. Every login carrying those claim values is denied. The migrations create only " +
					quoted("default") + "; create " + quoted(tenant) + " through the service API " +
					"(PUT /api/v2/service/tenants/" + tenant + ", enabled by auth.service_token), or map them to " + quoted("default"),
				Key:        tenantsKey,
				Value:      tenant,
				MissingKey: tenantsKey,
			})
			continue
		}
		out = append(out, missingRoleWarnings(ctx, ck, tenant, c.OIDC)...)
	}
	return out
}

// breakGlassAddressWarnings reports a break-glass allowlist whose addresses
// cannot actually sign in.
//
// The empty-list warning says nobody can get in. A non-empty list looks safe and
// is not. The gate admits the address (internal/api/auth_handler.go) and then
// FindUserByLogin finds no row and answers the same "invalid credentials" a
// wrong password gets, so the hatch does not open and nothing says why. That is
// worse than the empty case, because the operator believes they have one.
//
// The only local password user that ever exists is the bootstrap admin, and only
// when a bootstrap password was set at first install. Every other local user
// comes from the admin-authenticated user API, which is exactly what cannot be
// reached during a lock-out, so writing your own address into this list and
// expecting it to work is the natural mistake.
//
// A row is not enough. A user created by OIDC provisioning has a NULL password
// (the users_has_auth check permits it because the OIDC subject is the other
// half), so the row can exist while no password can ever verify against it.
//
// The tenant is "default" and not a configured one: the login page sends no
// tenant, and authTokenHandler defaults an empty one to "default", so that is
// the only tenant a break-glass login can ever land in.
//
// One working address is enough to get back in, so this warns only when NONE of
// them does, and then names all of them.
func breakGlassAddressWarnings(ctx context.Context, ck oidcNameChecker, emails []string) []configWarning {
	const key = "auth.oidc.break_glass_emails"
	if len(emails) == 0 {
		return nil // the empty case is oidcBreakGlassWarnings, with its own message
	}
	var failures []string
	for _, raw := range emails {
		email := strings.ToLower(strings.TrimSpace(raw))
		if email == "" {
			continue
		}
		ok, err := ck.LocalPasswordUserExists(ctx, breakGlassTenant, email)
		if err != nil {
			return []configWarning{{
				Msg: "could not check whether the addresses in " + key + " have a local password login: " + err.Error() +
					". An address listed there with no password row is an escape hatch that does not open, so this is " +
					"worth re-running by hand",
				Key:   key,
				Value: email,
			}}
		}
		if ok {
			return nil
		}
		failures = append(failures, email)
	}
	if len(failures) == 0 {
		return nil
	}
	return []configWarning{{
		Msg: key + " lists " + quotedList(failures) + ", and none of them has a local password login in the " +
			quoted(breakGlassTenant) + " tenant, so none of them can sign in. The allowlist admits the address and " +
			"the credential store then answers the same " + quoted("invalid credentials") + " a wrong password gets. " +
			"A user provisioned through single sign-on does not count: it has no password for anything to verify " +
			"against. Create the local account BEFORE you need it, while an admin session still exists",
		Key:        key,
		Value:      strings.Join(failures, ","),
		MissingKey: key,
	}}
}

// breakGlassTenant is the tenant a break-glass login always lands in: the login
// page sends no tenant and authTokenHandler defaults an empty one to this.
const breakGlassTenant = "default"

// missingRoleWarnings reports the role names configured for one existing tenant
// that the tenant does not have.
//
// The tenant is known to exist, so a lookup that comes back false means the role
// row is absent and resolveUser takes its !exists branch, denying the login as
// unknown_role:<role>. role_check_failed is the OTHER branch, the lookup itself
// erroring, which is what a MISSING TENANT produces (Repository.RoleExists
// resolves the tenant first and returns domain.ErrNotFound). Naming the wrong one
// sends an operator to grep the audit log for a reason that is not there.
//
// Each distinct role name is looked up once. Mapping several IdP groups to one
// Dexaflow role is the normal shape, and asking the same question per group is
// both a repeated round trip on the boot path and, when the answer is "missing",
// the same warning printed once per group.
func missingRoleWarnings(ctx context.Context, ck oidcNameChecker, tenant string, o config.OIDCSection) []configWarning {
	const mappingsKey = "auth.oidc.role_mappings"
	var out []configWarning
	type lookup struct {
		ok  bool
		err error
	}
	looked := map[string]lookup{}
	exists := func(role string) lookup {
		if l, done := looked[role]; done {
			return l
		}
		ok, err := ck.RoleExists(ctx, tenant, role)
		looked[role] = lookup{ok: ok, err: err}
		return looked[role]
	}
	// named describes where the role came from, for a message that points at a
	// line the operator can edit rather than at a role name alone.
	check := func(key, role, named string) {
		l := exists(role)
		switch {
		case l.err != nil:
			out = append(out, configWarning{
				Msg: "could not check whether the role " + quoted(role) + " named by " + key + " exists in the tenant " +
					quoted(tenant) + ": " + l.err.Error() + ". A login resolving to a role that does not exist is denied",
				Key:   key,
				Value: role,
			})
		case !l.ok:
			out = append(out, configWarning{
				Msg: key + " " + named + "the role " + quoted(role) + ", which the tenant " + quoted(tenant) +
					" does not have. A login that resolves to it is denied (audited unknown_role:" + role +
					") behind the same generic 403 as every other failure. The roles seeded for a new deployment are " +
					"viewer, editor, operator and admin",
				Key:        key,
				Value:      role,
				MissingKey: key,
			})
		}
	}
	// An unset default_role is a supported strict default-deny, not a name that
	// is missing, and oidcRoleSourceWarnings already says so. An EMPTY value
	// anywhere else is a name that does not exist: MapRoles copies an empty
	// mapped value straight into the resolved role set, and the login is denied
	// on it like any other unknown role.
	if o.DefaultRole != "" {
		check(defaultRoleKey, o.DefaultRole, "names ")
	}
	for role, groups := range keysByValue(o.RoleMappings) {
		check(mappingsKey, role, "maps "+quotedList(groups)+" to ")
	}
	return out
}

// keysByValue inverts a configured name→name map into one entry per distinct
// VALUE, carrying the keys that named it: the tenant and the claim values mapped
// to it, the role and the IdP groups mapped to it.
//
// The value is what gets looked up, so this is what makes "once per distinct
// name" structural rather than a deduplication bolted onto a per-key walk. The
// keys travel with it so the warning can name every line the operator has to
// edit. Entries come out in the order their first key sorts, and the keys within
// an entry in sorted order, so two boots log the same lines and a diff between
// them means something.
//
// An empty value is kept, not dropped. `corp.example:` with nothing after it is
// valid YAML that binds to "", nothing rejects it (validateOIDC checks only that
// tenant_claims is non-empty as a map), and it denies every login it touches.
// That is a name that does not exist, which is what this check is for.
func keysByValue(m map[string]string) iter.Seq2[string, []string] {
	keys := map[string][]string{}
	order := make([]string, 0, len(m))
	for k, v := range sortedPairs(m) {
		if _, seen := keys[v]; !seen {
			order = append(order, v)
		}
		keys[v] = append(keys[v], k)
	}
	return func(yield func(string, []string) bool) {
		for _, v := range order {
			if !yield(v, keys[v]) {
				return
			}
		}
	}
}

// quotedList renders names for an operator-facing message, quoted for the same
// reason a single one is.
func quotedList(names []string) string {
	quotedNames := make([]string, 0, len(names))
	for _, n := range names {
		quotedNames = append(quotedNames, quoted(n))
	}
	return strings.Join(quotedNames, ", ")
}

// sessionCookieWarnings reports the session cookie running without Secure. It
// is a documented setting, not an error, so boot proceeds: a deployment served
// over plain http to a name that is not loopback has no other way to sign
// anybody in, because a browser refuses a Secure cookie from such an origin
// outright. The WARN is the only operator-visible signal that the session token
// is now carried in the clear, and it carries the key and the value as fields
// so an alert rule does not have to substring-match prose.
func sessionCookieWarnings(c config.AuthSection) []configWarning {
	const key = "auth.session_cookie_insecure"
	if !c.SessionCookieInsecure {
		return nil
	}
	return []configWarning{{
		Msg: key + " is on, so the browser session cookie and the OIDC state cookie are sent without Secure: " +
			"the session token travels over plain http and anything on the path can read it and replay it. " +
			"Set this only on a deployment that genuinely cannot be reached over https; a loopback deployment " +
			"(localhost, 127.0.0.1) does not need it, because browsers accept a Secure cookie there already",
		Key:   key,
		Value: "true",
	}}
}

// oidcBreakGlassWarnings reports an SSO deployment with no way back in.
//
// Under provider: oidc, newBreakGlass with an empty allowlist returns a gate that
// admits nobody, so every password login is rejected. That is correct, and it is
// also the state in which any of the other failures here leaves NOBODY able to
// reach the control plane, including the operator who has to fix it. An IdP
// outage does the same, and neither is in this deployment's control.
func oidcBreakGlassWarnings(c config.AuthSection) []configWarning {
	const key = "auth.oidc.break_glass_emails"
	if c.Provider != config.AuthProviderOIDC || len(c.OIDC.BreakGlassEmails) > 0 {
		return nil
	}
	return []configWarning{{
		Msg: "auth.provider: oidc with an empty " + key + " rejects every password login, so an IdP outage or a " +
			"wrong tenant pin leaves nobody able to reach the control plane, including whoever has to fix it. " +
			"List at least one local admin address there (Helm: auth.oidc.breakGlassEmails), or keep a documented " +
			"route back to auth.provider: jwt",
		Key:        key,
		Value:      "",
		MissingKey: key,
	}}
}

// quoted renders a configured name for an operator-facing message. The quotes
// are the point: a trailing space or an invisible character in a YAML value is
// otherwise impossible to see in a log line.
func quoted(s string) string { return strconv.Quote(s) }

// sortedPairs iterates a string map in key order, so a boot log reads the same
// on every restart. Go randomizes map order, which would reshuffle the warnings
// each time and defeat a diff between two boots.
func sortedPairs(m map[string]string) iter.Seq2[string, string] {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return func(yield func(string, string) bool) {
		for _, k := range keys {
			if !yield(k, m[k]) {
				return
			}
		}
	}
}

// oidcJITWarnings reports an OIDC deployment with just-in-time provisioning off.
//
// The flag reads as "require a pre-provisioned user", but no pre-provisioning
// path exists. A login resolves a user ONLY through FindUserByOIDCSubject, so a
// row matches only if it carries oidc_provider and oidc_subject, and the sole
// statement that writes those two columns is CreateOIDCUser, reached only from
// the JIT path. No API endpoint, CLI command or migration writes them. With the
// flag off, every first login is denied (audited no_user_jit_off) and the
// operator has no supported way to create a user that would ever match.
//
// It is a WARN and not a boot failure because an operator who wrote the two
// columns directly with SQL has a working deployment, and boot must not break
// it. Closing the gap properly is linking an existing user by verified email
// (ADR 0057 D3), which is a behavior change and not an RC fix.
func oidcJITWarnings(c config.AuthSection) []configWarning {
	const key = "auth.oidc.jit_provisioning"
	if c.Provider != config.AuthProviderOIDC || c.OIDC.JITProvisioning {
		return nil
	}
	return []configWarning{{
		Msg: "auth.provider: oidc with " + key + " disabled denies every first login (audited no_user_jit_off): " +
			"a login matches a user only by (oidc_provider, oidc_subject), and the only statement that writes those " +
			"columns is the just-in-time provisioning this flag disables. No API, CLI or migration can pre-provision " +
			"an OIDC identity, so with this off nobody can sign in for the first time. Set " + key + " to true, " +
			"together with " + defaultRoleKey + " or auth.oidc.role_mappings so a provisioned user gets the roles you intend",
		Key:        key,
		Value:      "false",
		MissingKey: key,
	}}
}

// oidcClientSecretWarnings reports an OIDC deployment with no client secret.
//
// The secret is used in exactly one place: the authorization-code exchange
// (internal/oidc/flow.go). A confidential client rejects an exchange without it
// with invalid_client, and every managed IdP an operator is likely to use
// registers a server-side application as confidential: Google Workspace and
// Entra always issue one, Okta and Keycloak do unless the client is explicitly
// registered as public. The rejection arrives at the callback, which answers a
// generic 403 by design (the browser must not learn why a login was refused), so
// the operator sees a login that fails with nothing anywhere naming the secret.
//
// It is a WARN and not a boot failure because a public client (PKCE only, no
// secret) is a legitimate registration, and failing boot on an empty secret
// would break a deployment that works today.
func oidcClientSecretWarnings(c config.AuthSection) []configWarning {
	const key = "auth.oidc.client_secret"
	if c.Provider != config.AuthProviderOIDC || c.OIDC.ClientSecret != "" {
		return nil
	}
	return []configWarning{{
		Msg: "auth.provider: oidc with no " + key + ": the authorization-code exchange is sent without a client secret, " +
			"which a confidential client rejects with invalid_client. Google Workspace and Entra always register a server-side " +
			"application as confidential; Okta and Keycloak do unless the client is explicitly public. The callback answers a " +
			"generic 403 on failure, so the login breaks with nothing naming this. Set it via the " +
			"DEXAFLOW_AUTH_OIDC_CLIENT_SECRET environment variable (never in the config file), or ignore this if the IdP client " +
			"is registered as public",
		Key:        key,
		Value:      "",
		MissingKey: key,
	}}
}
