package oidc

import (
	"net/url"
	"testing"
)

// TestEndSessionURL is #1165: sign-out sends the browser to the discovered
// end_session_endpoint with the ID token as a hint, the client id, and the
// registered return address, keeping the query the endpoint already carries.
func TestEndSessionURL(t *testing.T) {
	f := newFakeIDP(t)
	f.endSession = true
	cfg := baseOIDCConfig(f)
	cfg.PostLogoutRedirectURL = "https://app.example.com/api/v2/auth/login?local=1"
	flow := newTestFlow(t, cfg)

	raw, ok := flow.EndSessionURL("the.id.token")
	if !ok {
		t.Fatal("EndSessionURL ok = false with a published end_session_endpoint")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != f.issuer+"/logout" {
		t.Errorf("endpoint = %q, want %q", got, f.issuer+"/logout")
	}
	q := u.Query()
	for k, want := range map[string]string{
		"tenant":                   "t1",
		"id_token_hint":            "the.id.token",
		"client_id":                cfg.ClientID,
		"post_logout_redirect_uri": cfg.PostLogoutRedirectURL,
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}

// TestEndSessionURLOmitsWhatIsNotThere sends no empty parameters: an IdP may
// refuse an empty id_token_hint or post_logout_redirect_uri outright.
func TestEndSessionURLOmitsWhatIsNotThere(t *testing.T) {
	f := newFakeIDP(t)
	f.endSession = true
	flow := newTestFlow(t, baseOIDCConfig(f))
	raw, ok := flow.EndSessionURL("")
	if !ok {
		t.Fatal("ok = false")
	}
	u, _ := url.Parse(raw)
	for _, k := range []string{"id_token_hint", "post_logout_redirect_uri"} {
		if u.Query().Has(k) {
			t.Errorf("%s sent empty: %s", k, raw)
		}
	}
}

// TestEndSessionURLWithoutEndpoint covers an issuer like Google that publishes
// no end_session_endpoint: there is nowhere to send the browser.
func TestEndSessionURLWithoutEndpoint(t *testing.T) {
	flow := newTestFlow(t, baseOIDCConfig(newFakeIDP(t)))
	if raw, ok := flow.EndSessionURL("the.id.token"); ok || raw != "" {
		t.Errorf("EndSessionURL = %q, %v; want none", raw, ok)
	}
	if flow.EndSessionEndpoint() != "" {
		t.Errorf("EndSessionEndpoint = %q, want empty", flow.EndSessionEndpoint())
	}
}

// TestEndSessionEndpointIsTheDiscoveredOne reports the published endpoint, for
// the boot log and the warning.
func TestEndSessionEndpointIsTheDiscoveredOne(t *testing.T) {
	f := newFakeIDP(t)
	f.endSession = true
	if got, want := newTestFlow(t, baseOIDCConfig(f)).EndSessionEndpoint(), f.issuer+"/logout?tenant=t1"; got != want {
		t.Errorf("EndSessionEndpoint = %q, want %q", got, want)
	}
}

// TestNewFlowRefusesAMalformedEndSessionEndpoint fails boot on an endpoint no
// browser could be sent to, rather than the first sign-out.
func TestNewFlowRefusesAMalformedEndSessionEndpoint(t *testing.T) {
	for _, raw := range []string{"/logout", "ftp://idp.example/logout", "https://"} {
		f := newFakeIDP(t)
		f.endSessionRaw = raw
		if _, err := NewFlow(t.Context(), baseOIDCConfig(f), testFlowSecret); err == nil {
			t.Errorf("end_session_endpoint %q: NewFlow = nil error, want a discovery failure", raw)
		}
	}
}
