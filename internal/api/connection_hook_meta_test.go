package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
)

// The connection-type catalog must render so the Add/Edit form is not empty: it
// drives every standard field. Regression for the "edit shows empty" bug.
func TestConnectionHookMeta(t *testing.T) {
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
	})
	rec := authGet(srv, http.MethodGet, "/ui/connections/hook_meta", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("hook_meta = %d", rec.Code)
	}
	var meta []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	var postgres map[string]any
	for _, m := range meta {
		if m["connection_type"] == "postgres" {
			postgres = m
		}
	}
	if postgres == nil {
		t.Fatalf("catalog missing postgres; got %d types", len(meta))
	}
	if postgres["hook_name"] != "Postgres" {
		t.Errorf("postgres hook_name = %v", postgres["hook_name"])
	}
	// standard_fields must carry the fields the form renders, incl. url_schema.
	sf, _ := postgres["standard_fields"].(map[string]any)
	for _, f := range []string{"host", "login", "password", "port", "url_schema", "description"} {
		if _, ok := sf[f]; !ok {
			t.Errorf("postgres standard_fields missing %q", f)
		}
	}
}

// TestConnectionHookMetaExtraFieldsFlexibleFormShape pins the DATA half of the
// ADR 0039 form-fidelity contract: a credential-rich connector (snowflake) serves
// its provider-specific custom fields in the exact param-spec shape the Airflow
// 3.2 SPA's FlexibleForm renders — each field is an object with a `schema` (whose
// `type` drives the input) and a `value`. (The visual render of these inputs is
// the one thing only a browser confirms; this guards everything up to the SPA.)
func TestConnectionHookMetaExtraFieldsFlexibleFormShape(t *testing.T) {
	srv := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
	})
	rec := authGet(srv, http.MethodGet, "/ui/connections/hook_meta", "")
	var meta []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatal(err)
	}
	var snowflake map[string]any
	for _, m := range meta {
		if m["connection_type"] == "snowflake" {
			snowflake = m
		}
	}
	if snowflake == nil {
		t.Fatal("catalog missing snowflake")
	}
	ef, _ := snowflake["extra_fields"].(map[string]any)
	if len(ef) == 0 {
		t.Fatal("snowflake extra_fields empty — the form would miss account/warehouse/role (ADR 0039 regression)")
	}
	for name, raw := range ef {
		field, ok := raw.(map[string]any)
		if !ok {
			t.Errorf("extra_field %q is not an object: %T", name, raw)
			continue
		}
		// FlexibleForm reads field.schema (with .type) + field.value.
		schema, ok := field["schema"].(map[string]any)
		if !ok || schema["type"] == nil {
			t.Errorf("extra_field %q missing the schema.type FlexibleForm needs: %v", name, field)
		}
		if _, ok := field["value"]; !ok {
			t.Errorf("extra_field %q missing `value` key", name)
		}
	}
}
