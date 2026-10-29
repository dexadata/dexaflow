// Package admissionwebhook is the dispatch admission hook that asks an
// operator's HTTPS endpoint (ADR 0100 §8). It sends each task attempt as JSON
// and turns the answer into an admission.Decision.
//
// The endpoint is trusted with the attempt's tenant, DAG and execution
// fields, never with its environment, source, secrets or token. Its answer is
// not trusted further than the hook contract allows: no redirect is followed,
// the body is capped at MaxResponseBytes and decoded strictly, the answer
// must echo the request's UID, and it may set a service account only when the
// operator's allowlist of service accounts is set.
package admissionwebhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dexadata/dexaflow/pkg/admission"
)

// APIVersion names the wire format. A breaking change to it takes a new
// version, served side by side.
const APIVersion = "dexaflow.io/admission/v1"

// Kind is the kind of both the request and the answer.
const Kind = "AdmissionReview"

// MaxResponseBytes caps the answer's body.
const MaxResponseBytes = 64 << 10

// Config is execution.admission.webhook.
type Config struct {
	// URL is the endpoint. It must be https unless its host is loopback.
	URL string
	// CAFile, when set, is a PEM bundle the endpoint's certificate must chain
	// to, instead of the system roots.
	CAFile string
	// TokenFile, when set, holds a bearer token sent as Authorization. It is
	// read on every call, so a rotated token is picked up without a restart.
	TokenFile string
	// AllowServiceAccount lets the answer set the service account. The server
	// sets it only when execution_allowlist.service_accounts is set, so the
	// endpoint's owner cannot move a task onto any account of the namespace.
	AllowServiceAccount bool
}

// Hook is the webhook admission hook. It is safe for concurrent use.
type Hook struct {
	url       string
	tokenFile string
	allowSA   bool
	client    *http.Client
}

var _ admission.Hook = (*Hook)(nil)

// ValidateURL checks the endpoint URL: absolute, https unless the host is
// loopback, and without user info (the token goes in token_file).
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("execution.admission.webhook.url: %w", err)
	}
	if u.Hostname() == "" || strings.ContainsAny(u.Host, " \t") || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("execution.admission.webhook.url %q must be an absolute https URL", raw)
	}
	if u.User != nil {
		return errors.New("execution.admission.webhook.url must not carry credentials; use execution.admission.webhook.token_file")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return fmt.Errorf("execution.admission.webhook.url %q must use https unless its host is loopback: the request carries tenant and DAG names", raw)
	}
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// New builds the hook. It fails when the URL is invalid or the CA bundle
// cannot be read, so a misconfigured server does not boot.
func New(c Config) (*Hook, error) {
	if err := ValidateURL(c.URL); err != nil {
		return nil, err
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("execution.admission.webhook.ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("execution.admission.webhook.ca_file %s: no PEM certificate found", c.CAFile)
		}
		tlsConfig.RootCAs = pool
	}
	if c.TokenFile != "" {
		if _, err := readToken(c.TokenFile); err != nil {
			return nil, err
		}
	}
	// The endpoint is the operator's, usually in the cluster: proxy
	// environment variables are not used (Proxy is nil). Buffered dispatch
	// calls the hook from several workers at once, so enough idle
	// connections are kept that a call rarely pays a TLS handshake inside
	// its timeout.
	transport := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     tlsConfig,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        64,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &Hook{
		url:       c.URL,
		tokenFile: c.TokenFile,
		allowSA:   c.AllowServiceAccount,
		client: &http.Client{
			Transport: transport,
			// The deadline comes from the dispatcher's context.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

func readToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("execution.admission.webhook.token_file: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", errors.New("execution.admission.webhook.token_file is empty")
	}
	return token, nil
}

// Admit asks the endpoint. Any failure to get a valid answer (transport,
// status, size, decoding, a UID mismatch, a service account it may not set)
// is an error: the dispatcher holds the attempt, or fails open when told to.
func (h *Hook) Admit(ctx context.Context, req *admission.Request) (admission.Decision, error) {
	uid := uuid.NewString()
	body, err := json.Marshal(review{APIVersion: APIVersion, Kind: Kind, Request: toWire(uid, req)})
	if err != nil {
		return admission.Decision{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, h.url, bytes.NewReader(body))
	if err != nil {
		return admission.Decision{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "application/json")
	if h.tokenFile != "" {
		token, terr := readToken(h.tokenFile)
		if terr != nil {
			// The error names a path of the control plane; the task only
			// learns the token is missing.
			slog.Error("admission webhook token unreadable", "error", terr)
			return admission.Decision{}, errors.New("webhook: token_file unreadable")
		}
		hreq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.client.Do(hreq)
	if err != nil {
		// A *url.Error names the endpoint and its address; the cause can
		// reach a task's failure, which tenants read, so it carries neither.
		slog.Warn("admission webhook request failed", "error", err)
		var ue *url.Error
		if errors.As(err, &ue) && ue.Timeout() {
			return admission.Decision{}, errors.New("webhook: request timed out")
		}
		return admission.Decision{}, errors.New("webhook: request failed")
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // best-effort close of the answer body
	if resp.StatusCode != http.StatusOK {
		//nolint:errcheck // draining the (capped) body for connection reuse; count/err are irrelevant
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBytes))
		return admission.Decision{}, fmt.Errorf("webhook: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return admission.Decision{}, fmt.Errorf("webhook: reading the answer: %w", err)
	}
	if len(raw) > MaxResponseBytes {
		return admission.Decision{}, fmt.Errorf("webhook: answer larger than %d bytes", MaxResponseBytes)
	}
	return h.decode(raw, uid)
}

func (h *Hook) decode(raw []byte, uid string) (admission.Decision, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var ans answer
	if err := dec.Decode(&ans); err != nil {
		return admission.Decision{}, fmt.Errorf("webhook: decoding the answer: %w", err)
	}
	if len(bytes.TrimSpace(raw[dec.InputOffset():])) > 0 {
		return admission.Decision{}, errors.New("webhook: the answer has data after its JSON object")
	}
	if ans.APIVersion != APIVersion || ans.Kind != Kind {
		return admission.Decision{}, fmt.Errorf("webhook: answer is %s %s, want %s %s", ans.APIVersion, ans.Kind, APIVersion, Kind)
	}
	r := ans.Response
	if r == nil {
		return admission.Decision{}, errors.New("webhook: the answer has no response")
	}
	if r.UID != uid {
		return admission.Decision{}, errors.New("webhook: the answer does not echo the request's uid")
	}
	switch r.Decision {
	case "admit":
		ch := r.Changes.toChanges()
		if ch.ServiceAccount != "" && !h.allowSA {
			return admission.Decision{}, errors.New("webhook: the answer sets a service account, which a webhook may do only when execution_allowlist.service_accounts is set")
		}
		if r.Reason != "" {
			return admission.Decision{}, errors.New("webhook: an admit answer carries no reason")
		}
		if ch.IsZero() {
			return admission.Admit(), nil
		}
		return admission.AdmitWith(ch), nil
	case "hold", "refuse":
		if r.Changes != nil {
			return admission.Decision{}, fmt.Errorf("webhook: a %s answer carries no changes", r.Decision)
		}
		if r.Decision == "hold" {
			return admission.Hold(r.Reason), nil
		}
		return admission.Refuse(r.Reason), nil
	default:
		return admission.Decision{}, fmt.Errorf("webhook: unknown decision %q (want admit, hold or refuse)", r.Decision)
	}
}
