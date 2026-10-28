package cioidc

import (
	"strings"
	"testing"
	"time"
)

// validIssuer is an entry Validate accepts.
func validIssuer() Issuer {
	return Issuer{
		Name:     "github-acme",
		Provider: ProviderGitHub,
		Issuer:   "https://token.actions.githubusercontent.com",
		Audience: "https://dexaflow.acme.example",
		Tenant:   "acme",
		Rules: []Rule{{
			Claims: map[string]string{"repository": "acme/dags", "ref": "refs/heads/main"},
			Role:   "deployer",
		}},
	}
}

func TestValidateAcceptsAValidList(t *testing.T) {
	gitlab := validIssuer()
	gitlab.Name, gitlab.Provider, gitlab.Issuer = "gitlab-acme", ProviderGitLab, "https://gitlab.com"
	gitlab.Rules = []Rule{{Claims: map[string]string{"project_path": "acme/dags", "ref_protected": "true"}, Role: "deployer"}}
	google := validIssuer()
	google.Name, google.Provider, google.Issuer = "cloudbuild-acme", ProviderCloudBuild, "https://accounts.google.com"
	google.Rules = []Rule{{Claims: map[string]string{"email": "builder@acme.iam.gserviceaccount.com"}, Role: "deployer",
		Scopes: []string{"dexaflow:deploy", "dexaflow:run"}}}
	other := validIssuer()
	other.Name, other.Provider, other.Issuer, other.JWKSURL = "buildkite", ProviderOther, "https://agent.buildkite.com", "https://agent.buildkite.com/.well-known/jwks"
	other.Rules = []Rule{{Claims: map[string]string{"organization_slug": "acme"}, Role: "deployer"}}
	other.MaxLifetime = 2 * time.Hour
	loopback := validIssuer()
	loopback.Name, loopback.Issuer, loopback.Audience = "local", "http://127.0.0.1:8200", "local"

	if err := Validate([]Issuer{validIssuer(), gitlab, google, other, loopback}); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if err := Validate(nil); err != nil {
		t.Errorf("Validate(nil): %v, want nil (CI OIDC off)", err)
	}
}

// TestValidateRefuses lists what fails at boot, each with the field the
// message must name.
func TestValidateRefuses(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Issuer)
		want   string
	}{
		{"bad name", func(i *Issuer) { i.Name = "GitHub Acme" }, "issuers[0].name"},
		{"unknown provider", func(i *Issuer) { i.Provider = "jenkins" }, "issuers[0].provider"},
		{"no issuer", func(i *Issuer) { i.Issuer = "" }, "issuers[0].issuer"},
		{"plain http issuer", func(i *Issuer) { i.Issuer = "http://token.example.com" }, "issuers[0].issuer"},
		{"plain http jwks", func(i *Issuer) { i.JWKSURL = "http://token.example.com/jwks" }, "issuers[0].jwks_url"},
		{"no audience", func(i *Issuer) { i.Audience = "" }, "issuers[0].audience"},
		{"audience with spaces", func(i *Issuer) { i.Audience = "a b" }, "issuers[0].audience"},
		{"no tenant", func(i *Issuer) { i.Tenant = "" }, "issuers[0].tenant"},
		{"negative lifetime", func(i *Issuer) { i.MaxLifetime = -time.Second }, "issuers[0].max_lifetime_seconds"},
		{"lifetime over a day", func(i *Issuer) { i.MaxLifetime = 25 * time.Hour }, "issuers[0].max_lifetime_seconds"},
		{"no rules", func(i *Issuer) { i.Rules = nil }, "issuers[0].rules"},
		{"rule without claims", func(i *Issuer) { i.Rules[0].Claims = nil }, "issuers[0].rules[0].claims"},
		{"empty claim name", func(i *Issuer) { i.Rules[0].Claims[""] = "x" }, "issuers[0].rules[0].claims"},
		{"empty pattern", func(i *Issuer) { i.Rules[0].Claims["ref"] = "" }, "issuers[0].rules[0].claims.ref"},
		{"bad pattern", func(i *Issuer) { i.Rules[0].Claims["ref"] = "refs/[" }, "issuers[0].rules[0].claims.ref"},
		{"pattern matching everything", func(i *Issuer) { i.Rules[0].Claims["repository"] = "*/*" }, "issuers[0].rules[0].claims.repository"},
		{"no identity claim", func(i *Issuer) { i.Rules[0].Claims = map[string]string{"ref": "refs/heads/main"} }, "issuers[0].rules[0].claims"},
		{"no role", func(i *Issuer) { i.Rules[0].Role = "" }, "issuers[0].rules[0].role"},
		{"admin role", func(i *Issuer) { i.Rules[0].Role = "admin" }, "issuers[0].rules[0].role"},
		{"unknown scope", func(i *Issuer) { i.Rules[0].Scopes = []string{"dexaflow:admin"} }, "issuers[0].rules[0].scopes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := validIssuer()
			tc.mutate(&is)

			err := Validate([]Issuer{is})

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %s", err, tc.want)
			}
		})
	}
}

func TestValidateRefusesDuplicates(t *testing.T) {
	sameName := validIssuer()
	sameName.Audience = "other"
	if err := Validate([]Issuer{validIssuer(), sameName}); err == nil || !strings.Contains(err.Error(), "issuers[1].name") {
		t.Errorf("two entries named github-acme: err = %v, want one naming issuers[1].name", err)
	}
	sameAudience := validIssuer()
	sameAudience.Name, sameAudience.Tenant = "github-globex", "globex"
	if err := Validate([]Issuer{validIssuer(), sameAudience}); err == nil || !strings.Contains(err.Error(), "issuers[1].audience") {
		t.Errorf("two entries with the same issuer and audience: err = %v, want one naming issuers[1].audience", err)
	}
}

// TestValidateIdentityClaimsPerProvider: each provider's rule must pin the
// claim that says who the job is, so no rule admits every repository.
func TestValidateIdentityClaimsPerProvider(t *testing.T) {
	cases := []struct {
		provider string
		ok, bad  string
	}{
		{ProviderGitHub, "repository_owner_id", "actor"},
		{ProviderGitLab, "namespace_path", "user_login"},
		{ProviderCloudBuild, "email", "azp"},
	}
	for _, c := range cases {
		is := validIssuer()
		is.Provider = c.provider
		is.Rules = []Rule{{Claims: map[string]string{c.ok: "acme"}, Role: "deployer"}}
		if err := Validate([]Issuer{is}); err != nil {
			t.Errorf("%s with %s: %v, want accepted", c.provider, c.ok, err)
		}
		is.Rules = []Rule{{Claims: map[string]string{c.bad: "acme"}, Role: "deployer"}}
		if err := Validate([]Issuer{is}); err == nil {
			t.Errorf("%s with only %s: accepted, want refused", c.provider, c.bad)
		}
	}
	other := validIssuer()
	other.Provider = ProviderOther
	other.Rules = []Rule{{Claims: map[string]string{"pipeline": "deploy-dags"}, Role: "deployer"}}
	if err := Validate([]Issuer{other}); err != nil {
		t.Errorf("other with any claim: %v, want accepted", err)
	}
}
