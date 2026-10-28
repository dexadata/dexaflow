package cioidc

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/dexadata/dexaflow/internal/auth"
)

// namePattern is what an entry may be called: it ends up in metric labels,
// session tokens and generated pipelines.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// providers are the values Issuer.Provider accepts.
var providers = []string{ProviderGitHub, ProviderGitLab, ProviderCloudBuild, ProviderOther}

// identityClaims are the claims that say which repository, project or
// service account a job belongs to, per provider. Every rule of an entry of
// that provider pins at least one, so no rule admits every job on the
// provider. ProviderOther has no list: the operator knows its claims.
var identityClaims = map[string][]string{
	ProviderGitHub:     {"repository", "repository_id", "repository_owner", "repository_owner_id"},
	ProviderGitLab:     {"project_path", "project_id", "namespace_path", "namespace_id"},
	ProviderCloudBuild: {"email", "sub"},
}

// scopes are the scopes a rule may grant.
var scopes = []string{auth.ScopeRead, auth.ScopeRun, auth.ScopeDeploy}

// Validate checks a list of entries, naming the first bad field as
// issuers[i].field. An empty list is valid: CI OIDC is off.
func Validate(issuers []Issuer) error {
	seenName := map[string]bool{}
	seenAud := map[string]bool{}
	for i, is := range issuers {
		at := fmt.Sprintf("issuers[%d]", i)
		if err := validateIssuer(at, is); err != nil {
			return err
		}
		if seenName[is.Name] {
			return fmt.Errorf("%s.name %q is used by an earlier entry; names are unique", at, is.Name)
		}
		key := is.Issuer + "\x00" + is.Audience
		if seenAud[key] {
			return fmt.Errorf("%s.audience %q is used with the same issuer by an earlier entry; give each tenant its own audience", at, is.Audience)
		}
		seenName[is.Name], seenAud[key] = true, true
	}
	return nil
}

// validateIssuer checks one entry's own fields and its rules.
func validateIssuer(at string, is Issuer) error {
	checks := []struct {
		ok   bool
		what string
	}{
		{namePattern.MatchString(is.Name), "name: 1 to 40 lowercase letters, digits or '-'"},
		{slices.Contains(providers, is.Provider), "provider: one of " + strings.Join(providers, ", ")},
		{urlAllowed(is.Issuer), "issuer: an https URL (http only on a loopback host)"},
		{is.JWKSURL == "" || urlAllowed(is.JWKSURL), "jwks_url: empty, or an https URL (http only on a loopback host)"},
		{is.Audience != "" && !strings.ContainsAny(is.Audience, " \t\r\n"), "audience: required, without spaces"},
		{is.Tenant != "", "tenant: required, the tenant the entry's tokens act in"},
		{is.MaxLifetime >= 0 && is.MaxLifetime <= MaxLifetimeCeiling, "max_lifetime_seconds: 0 (one hour) to 86400"},
		{len(is.Rules) > 0, "rules: at least one"},
	}
	for _, c := range checks {
		if !c.ok {
			return fmt.Errorf("%s.%s", at, c.what)
		}
	}
	for j, r := range is.Rules {
		if err := validateRule(fmt.Sprintf("%s.rules[%d]", at, j), is.Provider, r); err != nil {
			return err
		}
	}
	return nil
}

// validateRule checks one rule of an entry of provider.
func validateRule(at, provider string, r Rule) error {
	if len(r.Claims) == 0 {
		return fmt.Errorf("%s.claims: at least one claim to match", at)
	}
	for _, name := range sortedClaimNames(r.Claims) {
		if err := validatePattern(at, name, r.Claims[name]); err != nil {
			return err
		}
	}
	if want, ok := identityClaims[provider]; ok && !slices.ContainsFunc(want, func(c string) bool { _, has := r.Claims[c]; return has }) {
		return fmt.Errorf("%s.claims: a %s rule must match one of %s, or every job on the provider could deploy", at, provider, strings.Join(want, ", "))
	}
	switch r.Role {
	case "":
		return fmt.Errorf("%s.role: required, a role of the entry's tenant", at)
	case "admin":
		return fmt.Errorf("%s.role: a CI rule cannot grant admin; use deployer or a narrower custom role", at)
	}
	for _, s := range r.Scopes {
		if !slices.Contains(scopes, s) {
			return fmt.Errorf("%s.scopes: %q is not one of %s", at, s, strings.Join(scopes, ", "))
		}
	}
	return nil
}

// validatePattern checks the pattern of claim name: non-empty, parseable by
// path.Match, and not one that matches every value (a letter or a digit
// must be in it).
func validatePattern(at, name, pattern string) error {
	if name == "" {
		return fmt.Errorf("%s.claims: a claim name is empty", at)
	}
	if _, err := path.Match(pattern, ""); err != nil || pattern == "" {
		return fmt.Errorf("%s.claims.%s: %q is not a pattern (path.Match: * and ? within a segment, [ranges])", at, name, pattern)
	}
	if !strings.ContainsFunc(pattern, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' }) {
		return fmt.Errorf("%s.claims.%s: %q matches any value; name what it must be", at, name, pattern)
	}
	return nil
}

// sortedClaimNames returns the claim names of claims in order, so a
// message names the same claim on every boot.
func sortedClaimNames(claims map[string]string) []string {
	names := make([]string, 0, len(claims))
	for k := range claims {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

// urlAllowed reports whether raw is an https URL with a host, or an http URL
// on a loopback host (a provider in a test or on the same machine).
func urlAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		host := u.Hostname()
		ip := net.ParseIP(host)
		return host == "localhost" || (ip != nil && ip.IsLoopback())
	}
	return false
}
