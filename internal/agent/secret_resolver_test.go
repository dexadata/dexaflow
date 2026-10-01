package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/agent/secretsource"
)

func TestSubprocessResolverBatch(t *testing.T) {
	var gotStdin resolverInput
	r := &subprocessResolver{
		cfg: secretsource.BackendConfig{Class: "prov.Backend", Kwargs: json.RawMessage(`{"region_name":"us"}`)},
		run: func(_ context.Context, stdin []byte) ([]byte, error) {
			_ = json.Unmarshal(stdin, &gotStdin)
			return []byte(`{"connections":{"warehouse":"postgres://w"},"variables":{"region":"us"}}`), nil
		},
	}
	res, err := r.ResolveBatch(context.Background(), []secretsource.Ref{
		{Name: "region", Kind: secretsource.KindVariable},
		{Name: "warehouse", Kind: secretsource.KindConnection},
		{Name: "absent", Kind: secretsource.KindVariable},
	})
	if err != nil {
		t.Fatalf("ResolveBatch: %v", err)
	}
	// Request shape: class + kwargs carried, names split by kind.
	if gotStdin.Backend != "prov.Backend" || string(gotStdin.BackendKwargs) != `{"region_name":"us"}` {
		t.Errorf("stdin backend/kwargs wrong: %+v", gotStdin)
	}
	if len(gotStdin.Connections) != 1 || gotStdin.Connections[0] != "warehouse" {
		t.Errorf("connections in request: %v", gotStdin.Connections)
	}
	// Hits mapped back by kind; a miss (absent) is omitted.
	if res[secretsource.Ref{Name: "region", Kind: secretsource.KindVariable}] != "us" ||
		res[secretsource.Ref{Name: "warehouse", Kind: secretsource.KindConnection}] != "postgres://w" {
		t.Errorf("hits not mapped: %v", res)
	}
	if _, present := res[secretsource.Ref{Name: "absent", Kind: secretsource.KindVariable}]; present {
		t.Error("a miss must be omitted, not present")
	}
}

// A hard failure (non-zero exit) must return a SANITIZED error (no stderr/err
// passthrough — a backend may echo the secret) so the caller fails closed.
func TestSubprocessResolverHardErrorSanitized(t *testing.T) {
	r := &subprocessResolver{
		run: func(context.Context, []byte) ([]byte, error) {
			return nil, errors.New("AccessDenied: secret arn:aws:...:supersecret value=hunter2")
		},
	}
	_, err := r.ResolveBatch(context.Background(), []secretsource.Ref{{Name: "x", Kind: secretsource.KindVariable}})
	if err == nil {
		t.Fatal("hard error must fail closed")
	}
	if got := err.Error(); got == "" ||
		containsAny(got, "hunter2", "supersecret", "arn:aws") {
		t.Errorf("error must be sanitized (no secret/ARN passthrough): %q", got)
	}
}

// Malformed subprocess output fails closed (sanitized).
func TestSubprocessResolverMalformedOutput(t *testing.T) {
	r := &subprocessResolver{run: func(context.Context, []byte) ([]byte, error) { return []byte("not json"), nil }}
	if _, err := r.ResolveBatch(context.Background(), []secretsource.Ref{{Name: "x", Kind: secretsource.KindVariable}}); err == nil {
		t.Fatal("malformed output must fail closed")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
	}
	return false
}

func TestResolverFromEnv(t *testing.T) {
	// No backend configured → nil resolver, chain stays vault-only.
	res, _, err := ResolverFromEnv(func(string) string { return "" })
	if err != nil || res != nil {
		t.Errorf("no backend env must yield nil resolver, no error: res=%v err=%v", res, err)
	}

	// Configured → a resolver + routing derived from the kwargs prefixes.
	env := map[string]string{
		"LEOFLOW_SECRETS_BACKEND":        "prov.Backend",
		"LEOFLOW_SECRETS_BACKEND_KWARGS": `{"connections_prefix":"airflow/connections"}`,
	}
	res, backend, err := ResolverFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res == nil {
		t.Fatal("a configured backend must yield a resolver")
	}
	if !backend.Covers(secretsource.KindConnection) || backend.Covers(secretsource.KindVariable) {
		t.Error("routing must cover only the kinds whose prefix is set")
	}

	// Malformed kwargs → fail closed (error).
	bad := map[string]string{"LEOFLOW_SECRETS_BACKEND": "x", "LEOFLOW_SECRETS_BACKEND_KWARGS": "{oops"}
	if _, _, err := ResolverFromEnv(func(k string) string { return bad[k] }); err == nil {
		t.Error("malformed kwargs must fail closed")
	}
}

func TestResolverBaseEnvScrubsCredOverrides(t *testing.T) {
	in := []string{
		"PATH=/usr/bin", "HOME=/root", "LANG=C.UTF-8", // neutral base — survive
		"LEOFLOW_AGENT_TOKEN=secret",                               // stripped by stripAgentOnly
		"AWS_ROLE_ARN=arn:aws:iam::1:role/keyless",                 // keyless — survive
		"AWS_WEB_IDENTITY_TOKEN_FILE=/var/run/tok",                 // keyless — survive
		"AWS_CONTAINER_CREDENTIALS_FULL_URI=http://169.254.170.23", // keyless (Pod Identity) — survive
		"AWS_ACCESS_KEY_ID=AKIA_author",                            // static cred — scrubbed
		"AWS_SECRET_ACCESS_KEY=x",                                  // scrubbed
		"AWS_ENDPOINT_URL=http://evil",                             // endpoint override — scrubbed
		"AWS_PROFILE=hijack",                                       // scrubbed
		"AWS_DEFAULT_REGION=us-evil-1",                             // region redirect — scrubbed
		"GOOGLE_APPLICATION_CREDENTIALS=/k.json",                   // scrubbed
		"GCE_METADATA_HOST=evil.host",                              // ADC metadata redirect — scrubbed
		"HTTPS_PROXY=http://mitm",                                  // TLS MITM vector — scrubbed
		"https_proxy=http://mitm",                                  // lowercase (Python honors it) — scrubbed
		"NO_PROXY=x",                                               // *_PROXY — scrubbed
		"AWS_CA_BUNDLE=/author/ca.pem",                             // CA-trust override (MITM) — scrubbed
		"SSL_CERT_FILE=/author/ca.pem",                             // CA-trust override (MITM) — scrubbed
	}
	got := map[string]bool{}
	for _, kv := range resolverBaseEnv(in) {
		name, _, _ := strings.Cut(kv, "=")
		got[name] = true
	}
	for _, keep := range []string{"PATH", "HOME", "LANG", "AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_FULL_URI"} {
		if !got[keep] {
			t.Errorf("%s must survive (keyless/neutral)", keep)
		}
	}
	for _, drop := range []string{
		"LEOFLOW_AGENT_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_ENDPOINT_URL",
		"AWS_PROFILE", "AWS_DEFAULT_REGION", "GOOGLE_APPLICATION_CREDENTIALS", "GCE_METADATA_HOST",
		"HTTPS_PROXY", "https_proxy", "NO_PROXY", "AWS_CA_BUNDLE", "SSL_CERT_FILE",
	} {
		if got[drop] {
			t.Errorf("%s must be scrubbed from the resolver base env (author-influenceable cred/transport/CA vector)", drop)
		}
	}
}
