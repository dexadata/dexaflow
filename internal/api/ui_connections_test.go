package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeConnStore struct {
	conns    map[string]domain.Connection
	writeErr error
}

func (f *fakeConnStore) ListConnections(_ context.Context, _ string, _, _ int) ([]domain.Connection, int, error) {
	out := make([]domain.Connection, 0, len(f.conns))
	for _, c := range f.conns {
		out = append(out, c)
	}
	return out, len(out), nil
}

func (f *fakeConnStore) GetConnection(_ context.Context, _, id string) (domain.Connection, error) {
	if c, ok := f.conns[id]; ok {
		return c, nil
	}
	return domain.Connection{}, ErrNotFound
}

// SetConnectionPatch applies the tri-state patch to the in-memory store,
// mirroring the repository's COALESCE upsert: a nil field preserves the stored
// value, a non-nil field overwrites it (an empty string clears).
func (f *fakeConnStore) SetConnectionPatch(_ context.Context, _ string, p domain.ConnectionPatch) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.conns == nil {
		f.conns = map[string]domain.Connection{}
	}
	cur := f.conns[p.ConnID]
	cur.ConnID = p.ConnID
	cur.ConnType = p.ConnType
	if p.Host != nil {
		cur.Host = *p.Host
	}
	if p.Login != nil {
		cur.Login = *p.Login
	}
	if p.Schema != nil {
		cur.Schema = *p.Schema
	}
	if p.Password != nil {
		cur.Password = *p.Password
	}
	if p.Port != nil {
		cur.Port = p.Port
	}
	if p.Extra != nil {
		cur.Extra = *p.Extra
	}
	if p.Description != nil {
		cur.Description = *p.Description
	}
	f.conns[p.ConnID] = cur
	return nil
}

func (f *fakeConnStore) DeleteConnection(_ context.Context, _, id string) error {
	if _, ok := f.conns[id]; !ok {
		return ErrNotFound
	}
	delete(f.conns, id)
	return nil
}

func connServer(store ConnectionStore) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Connections:   store,
	})
}

func TestConnectionCRUDNeverReturnsPassword(t *testing.T) {
	store := &fakeConnStore{conns: map[string]domain.Connection{}}
	srv := connServer(store)

	body := `{"connection_id":"pg","conn_type":"postgres","host":"db","login":"u","password":"s3cr3t","schema":"public","port":5432,"extra":"{\"sslmode\":\"require\"}"}`
	rec := authGet(srv, http.MethodPost, "/api/v2/connections", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	// The store received the password (to encrypt), but the response must not echo it.
	if store.conns["pg"].Password != "s3cr3t" {
		t.Errorf("store should receive the password to encrypt, got %q", store.conns["pg"].Password)
	}
	if strings.Contains(rec.Body.String(), "s3cr3t") || strings.Contains(rec.Body.String(), "password") {
		t.Errorf("response leaked the password: %s", rec.Body.String())
	}

	// Get also never returns the password.
	rec = authGet(srv, http.MethodGet, "/api/v2/connections/pg", "")
	if strings.Contains(rec.Body.String(), "password") || strings.Contains(rec.Body.String(), "s3cr3t") {
		t.Errorf("get leaked the password: %s", rec.Body.String())
	}
	var dto connectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.ConnectionID != "pg" || dto.ConnType != "postgres" || dto.Port == nil || *dto.Port != 5432 {
		t.Errorf("unexpected connection dto: %+v", dto)
	}

	// Delete.
	if rec := authGet(srv, http.MethodDelete, "/api/v2/connections/pg", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}
	if rec := authGet(srv, http.MethodGet, "/api/v2/connections/pg", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", rec.Code)
	}
}

// TestConnectionGetMasksSensitiveExtra: the read API must not echo secrets that
// ride in a connection's `extra` (#11). A Databricks OAuth connection keeps its
// client_secret/token there, and BigQuery its keyfile_dict — GET previously
// returned them in clear, which cost a credential rotation in the field.
func TestConnectionGetMasksSensitiveExtra(t *testing.T) {
	store := &fakeConnStore{conns: map[string]domain.Connection{
		"wh": {
			ConnID: "wh", ConnType: "databricks",
			Extra: `{"client_secret":"shhh","token":"tok-123","http_path":"/sql/1.0/x","account":"acme","keyfile_dict":"{\"p\":\"k\"}"}`,
		},
	}}
	srv := connServer(store)

	rec := authGet(srv, http.MethodGet, "/api/v2/connections/wh", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leaked := range []string{"shhh", "tok-123"} {
		if strings.Contains(body, leaked) {
			t.Errorf("GET leaked a secret from extra (%q): %s", leaked, body)
		}
	}
	var dto connectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Extra == nil {
		t.Fatal("extra should still be present (masked, not dropped)")
	}
	var ex map[string]any
	if err := json.Unmarshal([]byte(*dto.Extra), &ex); err != nil {
		t.Fatalf("extra not valid json: %v", err)
	}
	if ex["client_secret"] != "***" || ex["token"] != "***" || ex["keyfile_dict"] != "***" {
		t.Errorf("sensitive extra keys must be masked to ***: %v", ex)
	}
	if ex["http_path"] != "/sql/1.0/x" || ex["account"] != "acme" {
		t.Errorf("non-sensitive extra keys must survive: %v", ex)
	}
}

// TestRedactExtraNestedAndFailClosed: secrets nested under a non-sensitive key
// are still masked (extra is free-form), and a non-object extra fails closed.
func TestRedactExtraNestedAndFailClosed(t *testing.T) {
	nested := redactExtra(`{"config":{"client_secret":"deep"},"opts":[{"token":"t"}],"schema":"public"}`)
	if strings.Contains(nested, "deep") || strings.Contains(nested, "\"t\"") {
		t.Errorf("nested secrets must be masked: %s", nested)
	}
	if !strings.Contains(nested, "public") {
		t.Errorf("nested non-secret must survive: %s", nested)
	}
	if got := redactExtra(`"a-bare-secret-string"`); got != "***" {
		t.Errorf("non-object extra must fail closed to ***, got %q", got)
	}
	if got := redactExtra(""); got != "" {
		t.Errorf("empty extra stays empty, got %q", got)
	}
}

func TestConnectionWriteWithoutKeyReturns503(t *testing.T) {
	// The store reports no encryption key -> the API refuses the write (never
	// stores a credential in plaintext), surfaced as 503.
	store := &fakeConnStore{conns: map[string]domain.Connection{}, writeErr: errors.New("no encryption key configured")}
	rec := authGet(connServer(store), http.MethodPost, "/api/v2/connections",
		`{"connection_id":"pg","conn_type":"postgres","password":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("write without key = %d, want 503", rec.Code)
	}
}

// TestConnectionUpdateTriState locks the #887 write semantics on the PATCH path:
// an omitted field preserves the stored value, an explicit "" clears it, and a
// masked password ("***") is treated as unchanged so a round-tripped GET never
// overwrites the real secret with the mask (#874).
func TestConnectionUpdateTriState(t *testing.T) {
	newStore := func() *fakeConnStore {
		return &fakeConnStore{conns: map[string]domain.Connection{
			"pg": {
				ConnID: "pg", ConnType: "postgres", Host: "db", Login: "analytics",
				Password: "s3cr3t", Schema: "public", Extra: `{"sslmode":"require"}`,
			},
		}}
	}

	t.Run("omitted field preserves", func(t *testing.T) {
		store := newStore()
		srv := connServer(store)
		rec := authGet(srv, http.MethodPatch, "/api/v2/connections/pg",
			`{"conn_type":"postgres","host":"db2"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		got := store.conns["pg"]
		if got.Host != "db2" {
			t.Errorf("host not updated: %q", got.Host)
		}
		if got.Login != "analytics" || got.Password != "s3cr3t" || got.Extra != `{"sslmode":"require"}` {
			t.Errorf("omitted fields were clobbered: %+v", got)
		}
	})

	t.Run("explicit empty clears a non-secret field", func(t *testing.T) {
		store := newStore()
		srv := connServer(store)
		rec := authGet(srv, http.MethodPatch, "/api/v2/connections/pg",
			`{"conn_type":"postgres","login":""}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		if got := store.conns["pg"]; got.Login != "" {
			t.Errorf("login was not cleared: %q", got.Login)
		}
		if got := store.conns["pg"]; got.Password != "s3cr3t" {
			t.Errorf("clearing login must not touch the password: %q", got.Password)
		}
	})

	t.Run("masked password preserves the real secret", func(t *testing.T) {
		store := newStore()
		srv := connServer(store)
		rec := authGet(srv, http.MethodPatch, "/api/v2/connections/pg",
			`{"conn_type":"postgres","password":"***","host":"db3"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		got := store.conns["pg"]
		if got.Password != "s3cr3t" {
			t.Errorf("masked password overwrote the real secret: %q", got.Password)
		}
		if got.Host != "db3" {
			t.Errorf("the co-submitted host edit did not land: %q", got.Host)
		}
	})

	t.Run("explicit empty password clears it", func(t *testing.T) {
		store := newStore()
		srv := connServer(store)
		rec := authGet(srv, http.MethodPatch, "/api/v2/connections/pg",
			`{"conn_type":"postgres","password":""}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		if got := store.conns["pg"]; got.Password != "" {
			t.Errorf("explicit empty password did not clear: %q", got.Password)
		}
	})
}

// TestConnectionUpdateExtraUnmask locks that a round-tripped `extra` (secrets
// shown as the mask by GET) is merged back against the stored blob rather than
// persisting the mask over the real secret (#874), while a co-edited plain key
// still lands. It also covers the wholesale-mask case and an explicit clear.
func TestConnectionUpdateExtraUnmask(t *testing.T) {
	seed := func() *fakeConnStore {
		return &fakeConnStore{conns: map[string]domain.Connection{
			"wh": {
				ConnID: "wh", ConnType: "databricks",
				Extra: `{"token":"tok-REAL","http_path":"/sql/1.0/x","account":"acme"}`,
			},
		}}
	}

	t.Run("masked secret key is restored, plain edit lands", func(t *testing.T) {
		store := seed()
		srv := connServer(store)
		// The UI GET'd the connection (token shown as ***) and the user changed
		// http_path, then re-submitted the whole extra.
		rec := authGet(srv, http.MethodPatch, "/api/v2/connections/wh",
			`{"conn_type":"databricks","extra":"{\"token\":\"***\",\"http_path\":\"/sql/2.0/y\",\"account\":\"acme\"}"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		var ex map[string]any
		if err := json.Unmarshal([]byte(store.conns["wh"].Extra), &ex); err != nil {
			t.Fatalf("stored extra not json: %v (%q)", err, store.conns["wh"].Extra)
		}
		if ex["token"] != "tok-REAL" {
			t.Errorf("masked token overwrote the real secret: %v", ex["token"])
		}
		if ex["http_path"] != "/sql/2.0/y" {
			t.Errorf("the plain edit did not land: %v", ex["http_path"])
		}
	})

	t.Run("wholesale mask preserves the stored extra", func(t *testing.T) {
		store := seed()
		srv := connServer(store)
		rec := authGet(srv, http.MethodPatch, "/api/v2/connections/wh",
			`{"conn_type":"databricks","extra":"***"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		if got := store.conns["wh"].Extra; got != `{"token":"tok-REAL","http_path":"/sql/1.0/x","account":"acme"}` {
			t.Errorf("wholesale mask did not preserve the stored extra: %q", got)
		}
	})

	t.Run("explicit empty extra clears it", func(t *testing.T) {
		store := seed()
		srv := connServer(store)
		rec := authGet(srv, http.MethodPatch, "/api/v2/connections/wh",
			`{"conn_type":"databricks","extra":""}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		if got := store.conns["wh"].Extra; got != "" {
			t.Errorf("explicit empty extra did not clear: %q", got)
		}
	})

	t.Run("secret nested in an array is restored, top-level edit lands", func(t *testing.T) {
		store := &fakeConnStore{conns: map[string]domain.Connection{
			"wh": {
				ConnID: "wh", ConnType: "databricks",
				// A secret buried in an array-of-objects (redactExtra masks these too).
				Extra: `{"account":"acme","hosts":[{"password":"arr-REAL"}]}`,
			},
		}}
		srv := connServer(store)
		// UI GET'd it (array password shown as ***) and edited the top-level account.
		rec := authGet(srv, http.MethodPatch, "/api/v2/connections/wh",
			`{"conn_type":"databricks","extra":"{\"account\":\"beta\",\"hosts\":[{\"password\":\"***\"}]}"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		var ex map[string]any
		if err := json.Unmarshal([]byte(store.conns["wh"].Extra), &ex); err != nil {
			t.Fatalf("stored extra not json: %v (%q)", err, store.conns["wh"].Extra)
		}
		if ex["account"] != "beta" {
			t.Errorf("top-level edit was dropped (whole field force-preserved?): %v", ex["account"])
		}
		hosts, _ := ex["hosts"].([]any)
		if len(hosts) != 1 {
			t.Fatalf("array shape changed: %v", ex)
		}
		first, _ := hosts[0].(map[string]any)
		if first["password"] != "arr-REAL" {
			t.Errorf("array-nested secret overwritten by the mask: %v", first)
		}
	})
}

// TestConnectionUpsertPOSTMaskedRoundTrip locks that the POST upsert path (what
// `dexaflow connections set` uses) merges a masked field against the EXISTING
// connection, not an empty one. A regression here silently wiped a round-tripped
// secret extra to "{}" even though PATCH was correct (caught by the e2e).
func TestConnectionUpsertPOSTMaskedRoundTrip(t *testing.T) {
	store := &fakeConnStore{conns: map[string]domain.Connection{
		"pg": {
			ConnID: "pg", ConnType: "postgres", Host: "db",
			Password: "s3cr3t", Extra: `{"token":"tok-REAL"}`,
		},
	}}
	srv := connServer(store)
	// Re-POST the same connection with a masked password and masked extra.
	rec := authGet(srv, http.MethodPost, "/api/v2/connections",
		`{"connection_id":"pg","conn_type":"postgres","password":"***","extra":"{\"token\":\"***\"}"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post = %d (%s)", rec.Code, rec.Body.String())
	}
	got := store.conns["pg"]
	if got.Password != "s3cr3t" {
		t.Errorf("POST upsert overwrote the real password with the mask: %q", got.Password)
	}
	if got.Extra != `{"token":"tok-REAL"}` {
		t.Errorf("POST upsert wiped/masked the real extra token: %q", got.Extra)
	}
}

// TestUnmaskExtra unit-tests the fail-closed merge helper directly.
func TestUnmaskExtra(t *testing.T) {
	stored := `{"token":"real","cfg":{"secret":"deep"},"opts":["a"]}`

	// A masked key is restored from stored; a plain key passes through.
	got, preserve := unmaskExtra(`{"token":"***","region":"eu"}`, stored)
	if preserve {
		t.Fatal("a resolvable object must not preserve wholesale")
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(got), &m)
	if m["token"] != "real" || m["region"] != "eu" {
		t.Errorf("unmask/merge wrong: %v", m)
	}

	// Nested masked key restored from the stored nested object.
	got, _ = unmaskExtra(`{"cfg":{"secret":"***"}}`, stored)
	_ = json.Unmarshal([]byte(got), &m)
	cfg, _ := m["cfg"].(map[string]any)
	if cfg["secret"] != "deep" {
		t.Errorf("nested unmask wrong: %v", m)
	}

	// Wholesale mask preserves.
	if _, preserve = unmaskExtra("***", stored); !preserve {
		t.Error("wholesale mask must preserve")
	}

	// A mask with no stored counterpart is dropped (never persisted literally).
	got, preserve = unmaskExtra(`{"ghost":"***","keep":"v"}`, stored)
	if preserve {
		t.Fatal("a droppable mask should not force wholesale preserve")
	}
	_ = json.Unmarshal([]byte(got), &m)
	if _, ok := m["ghost"]; ok {
		t.Errorf("unresolvable mask must be dropped, not persisted: %v", m)
	}

	// A secret nested inside an array-of-objects is restored from the stored array
	// (aligned by index), and a co-edited plain key at the top level still lands —
	// the whole field must NOT be force-preserved.
	arrStored := `{"region":"us-east","items":[{"token":"real-arr","note":"n"}]}`
	got, preserve = unmaskExtra(`{"region":"eu-west","items":[{"token":"***","note":"n"}]}`, arrStored)
	if preserve {
		t.Fatal("a restorable array-nested mask must not force wholesale preserve")
	}
	_ = json.Unmarshal([]byte(got), &m)
	if m["region"] != "eu-west" {
		t.Errorf("top-level co-edit lost when a mask sat in an array: %v", m)
	}
	if items, _ := m["items"].([]any); len(items) != 1 {
		t.Fatalf("array shape changed: %v", m)
	} else if first, _ := items[0].(map[string]any); first["token"] != "real-arr" {
		t.Errorf("array-nested secret not restored from stored: %v", m)
	}

	// A masked array element the stored side cannot supply (shorter/empty array)
	// fails closed to preserve — never a silent drop that shifts indices.
	if _, preserve = unmaskExtra(`{"items":[{"token":"***"}]}`, `{"items":[]}`); !preserve {
		t.Error("an unresolvable array-nested mask must fail closed to preserve")
	}
	// A bare masked string in an array with no stored counterpart also fails closed.
	if _, preserve = unmaskExtra(`{"opts":["***"]}`, `{"opts":[]}`); !preserve {
		t.Error("an unresolvable bare array mask must fail closed to preserve")
	}

	// An explicit empty extra clears (not the mask, not an object).
	if got, preserve := unmaskExtra("", stored); preserve || got != "" {
		t.Errorf(`empty extra must clear: got %q preserve=%v`, got, preserve)
	}
}

func TestConnectionsEmptyStubWithoutStore(t *testing.T) {
	rec := authGet(connServer(nil), http.MethodGet, "/api/v2/connections", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("nil store = %d", rec.Code)
	}
	var col map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &col)
	if col["total_entries"].(float64) != 0 {
		t.Errorf("nil store should yield empty collection, got %v", col)
	}
}
