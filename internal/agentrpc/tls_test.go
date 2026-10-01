package agentrpc_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// genCert writes a self-signed cert+key valid for 127.0.0.1 to dir and returns
// the cert and key paths (the cert doubles as its own CA for the client).
func genCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "leoflow-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// fakeSecretsTLS is a minimal SecretsStore for the TLS round-trip.
type fakeSecretsTLS struct{}

func (fakeSecretsTLS) SecretVariables(context.Context, string) (map[string]string, error) {
	return map[string]string{"FOO": "bar"}, nil
}
func (fakeSecretsTLS) SecretConnectionURIs(context.Context, string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (fakeSecretsTLS) SecretVariablesScoped(context.Context, string, []string) (map[string]string, error) {
	return map[string]string{"FOO": "bar"}, nil
}
func (fakeSecretsTLS) SecretConnectionURIsScoped(context.Context, string, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

type tlsFakeStore struct{}

func (tlsFakeStore) TaskSpec(context.Context, auth.AgentIdentity) (agentrpc.TaskSpec, error) {
	return agentrpc.TaskSpec{}, nil
}
func (tlsFakeStore) ReportState(context.Context, auth.AgentIdentity, domain.TaskState, int, string) error {
	return nil
}
func (tlsFakeStore) Reschedule(context.Context, auth.AgentIdentity, time.Time) error { return nil }
func (tlsFakeStore) RecordHeartbeat(context.Context, auth.AgentIdentity) error       { return nil }
func (tlsFakeStore) BindWarmAttempt(context.Context, string, string, int, string) error {
	return nil
}

type tlsNoXCom struct{}

func (tlsNoXCom) Push(context.Context, xcom.Key, []byte, string, map[string]any) error { return nil }
func (tlsNoXCom) Fetch(context.Context, xcom.Key) (xcom.Entry, error) {
	return xcom.Entry{}, xcom.ErrNotFound
}

// TestSecretsOverTLSWithoutInsecureFlag proves that with a real TLS channel the
// secrets gate passes even though allowInsecure=false — i.e. enabling gRPC TLS
// (#58) removes the need for the dev insecure flag.
func TestSecretsOverTLSWithoutInsecureFlag(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath := genCert(t, dir)

	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	srv := agentrpc.NewServer(authn, tlsFakeStore{}, tlsNoXCom{})
	srv.SetSecrets(fakeSecretsTLS{}, false) // require a secure channel

	creds, err := credentials.NewServerTLSFromFile(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer(grpc.Creds(creds))
	agentv1.RegisterAgentServiceServer(gs, srv)
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	token, err := authn.IssueAgentToken(auth.AgentIdentity{TenantID: "acme", TaskInstanceID: "ti"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// allowInsecure=false + the cert as the CA → verified TLS.
	client, conn, _, err := agent.Dial(lis.Addr().String(), token, false, certPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables over TLS = %v, want success (no insecure flag needed)", err)
	}
	if resp.GetVariables()["FOO"] != "bar" {
		t.Errorf("variables = %v", resp.GetVariables())
	}
}
