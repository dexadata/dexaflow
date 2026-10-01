//go:build integration

package storage_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
)

// TestSMTPConnectionURIShapeIntegration walks the full delivery chain for an
// smtp Connection: Repository -> SecretConnectionURIs -> URI shape. smtp
// carries a host:port mail relay, a login/password, and an Extra blob
// (from_email, timeout) the SmtpHook reads. The password uses URI-reserved
// characters to pin the percent-escape round-trip, and the Extra blob must
// survive under __extra__ — a regression there would silently drop the
// from_email / timeout the hook depends on.
func TestSMTPConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 151)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra    = `{"from_email":"bot@example.com","timeout":30}`
	)
	connID := fmt.Sprintf("e2e_smtp_%d", time.Now().UnixNano())
	port := 587
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "smtp",
		Host:     "smtp.example.com",
		Login:    "etl_user",
		Password: rawPassword,
		Port:     &port,
		Extra:    rawExtra,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "smtp" {
		t.Errorf("scheme = %q, want smtp", parsed.Scheme)
	}
	if want := "smtp.example.com:587"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
	if gotExtra := parsed.Query().Get("__extra__"); gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestIMAPConnectionURIShapeIntegration walks the full delivery chain for an
// imap Connection. imap is a plain host:port mailbox with a login/password and
// no Extra; the ImapHook needs the user and password verbatim, so this pins
// the scheme, the host:port shape, the username, and the URI-reserved-character
// password round-trip.
func TestIMAPConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 157)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	connID := fmt.Sprintf("e2e_imap_%d", time.Now().UnixNano())
	port := 993
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "imap",
		Host:     "imap.example.com",
		Login:    "etl_user",
		Password: rawPassword,
		Port:     &port,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "imap" {
		t.Errorf("scheme = %q, want imap", parsed.Scheme)
	}
	if want := "imap.example.com:993"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestOpsgenieConnectionURIShapeIntegration walks the full delivery chain for
// an opsgenie Connection. Opsgenie authenticates with an API key (no login,
// no port): the key lives in the password field and carries URI-reserved
// characters, so this pins the scheme, the bare host, and the password
// round-trip the OpsgenieAlertHook depends on.
func TestOpsgenieConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 163)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawPassword = "api-key/1+2" //nolint:gosec // hardcoded test fixture, not a credential
	connID := fmt.Sprintf("e2e_opsgenie_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "opsgenie",
		Host:     "api.opsgenie.com",
		Password: rawPassword,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "opsgenie" {
		t.Errorf("scheme = %q, want opsgenie", parsed.Scheme)
	}
	if want := "api.opsgenie.com"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}

// TestZendeskConnectionURIShapeIntegration walks the full delivery chain for a
// zendesk Connection. Zendesk uses an agent email as login, an API token as
// password (URI-reserved characters), and an Extra blob (token, use_token) the
// ZendeskHook reads. This pins the scheme, host, password round-trip, and the
// __extra__ round-trip.
func TestZendeskConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 167)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawPassword = "api-token/1+2" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra    = `{"token":"tok/en+1","use_token":true}`
	)
	connID := fmt.Sprintf("e2e_zendesk_%d", time.Now().UnixNano())
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "zendesk",
		Host:     "company.zendesk.com",
		Login:    "agent@example.com",
		Password: rawPassword,
		Extra:    rawExtra,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "zendesk" {
		t.Errorf("scheme = %q, want zendesk", parsed.Scheme)
	}
	if want := "company.zendesk.com"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
	if gotExtra := parsed.Query().Get("__extra__"); gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestSambaConnectionURIShapeIntegration walks the full delivery chain for a
// samba Connection. Samba is a host:port file share with a login/password and
// an Extra blob (share_type) the SambaHook reads. This pins the scheme, the
// host:port shape, the URI-reserved-character password round-trip, and the
// __extra__ round-trip.
func TestSambaConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 173)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const (
		rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
		rawExtra    = `{"share_type":"smb2"}`
	)
	connID := fmt.Sprintf("e2e_samba_%d", time.Now().UnixNano())
	port := 445
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "samba",
		Host:     "files.example.com",
		Login:    "etl_user",
		Password: rawPassword,
		Port:     &port,
		Extra:    rawExtra,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "samba" {
		t.Errorf("scheme = %q, want samba", parsed.Scheme)
	}
	if want := "files.example.com:445"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
	if gotExtra := parsed.Query().Get("__extra__"); gotExtra != rawExtra {
		t.Errorf("__extra__ round-trip failed: got %q, want %q", gotExtra, rawExtra)
	}
}

// TestGCPSSHConnectionURIShapeIntegration walks the full delivery chain for a
// gcpssh Connection. The ComputeEngineSSHHook reaches a Compute Engine VM over
// SSH: a host:port endpoint with a login/password. This pins the scheme, the
// host:port shape, the username, and the URI-reserved-character password
// round-trip.
func TestGCPSSHConnectionURIShapeIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 179)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	const rawPassword = "p@ss/w0rd:!#$" //nolint:gosec // hardcoded test fixture, not a credential
	connID := fmt.Sprintf("e2e_gcpssh_%d", time.Now().UnixNano())
	port := 22
	if cerr := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "gcpssh",
		Host:     "10.0.0.5",
		Login:    "etl_user",
		Password: rawPassword,
		Port:     &port,
	}); cerr != nil {
		t.Fatalf("SetConnection: %v", cerr)
	}
	t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", connID) })

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs: %v", err)
	}
	uri, present := uris[connID]
	if !present {
		t.Fatalf("URI for %q missing from delivery map; got keys = %v", connID, mapKeys(uris))
	}

	parsed, perr := url.Parse(uri)
	if perr != nil {
		t.Fatalf("URI is not parseable (the Python connector would fail): %q err=%v", uri, perr)
	}
	if parsed.Scheme != "gcpssh" {
		t.Errorf("scheme = %q, want gcpssh", parsed.Scheme)
	}
	if want := "10.0.0.5:22"; parsed.Host != want {
		t.Errorf("host = %q, want %q", parsed.Host, want)
	}
	if parsed.User.Username() != "etl_user" {
		t.Errorf("username = %q, want etl_user", parsed.User.Username())
	}
	gotPassword, _ := parsed.User.Password()
	if gotPassword != rawPassword {
		t.Errorf("password round-trip failed: got %q, want %q", gotPassword, rawPassword)
	}
}
