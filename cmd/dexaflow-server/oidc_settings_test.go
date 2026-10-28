package main

import (
	"reflect"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestOIDCSettingsCarriesEveryField locks that oidcSettings copies each
// field of api.OIDCSettings from the auth.oidc field of the same name, so a
// field added to one side is not silently left at its zero value.
func TestOIDCSettingsCarriesEveryField(t *testing.T) {
	o := config.OIDCSection{
		Issuer:           "https://idp.example",
		RedirectURL:      "https://app.example/api/v2/auth/oidc/callback",
		RoleMappings:     map[string]string{"data-eng": "editor"},
		DefaultRole:      "viewer",
		BreakGlassEmails: []string{"root@example.com"},
		JITProvisioning:  true,
		AutoRedirect:     true,
	}
	got := reflect.ValueOf(oidcSettings(o))
	src := reflect.ValueOf(o)
	for i := range got.NumField() {
		name := got.Type().Field(i).Name
		want := src.FieldByName(name)
		if !want.IsValid() {
			t.Fatalf("api.OIDCSettings.%s has no auth.oidc field of that name", name)
		}
		if want.IsZero() {
			t.Fatalf("the test sets no value for auth.oidc %s", name)
		}
		if !reflect.DeepEqual(got.Field(i).Interface(), want.Interface()) {
			t.Errorf("%s = %v, want %v", name, got.Field(i).Interface(), want.Interface())
		}
	}
}
