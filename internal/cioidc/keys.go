package cioidc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"

	"github.com/dexadata/dexaflow/internal/issuer"
)

// discoveryCooldown is the least time between two downloads of a discovery
// document after one failed, so a provider outage does not turn every
// exchange into an outbound call.
const discoveryCooldown = 30 * time.Second

// discoveryTimeout bounds one discovery download, maxDiscoveryBytes its body.
const (
	discoveryTimeout  = 10 * time.Second
	maxDiscoveryBytes = 1 << 20
)

// lazyKeys is an entry's key set: the configured JWKS, or the one its
// discovery document names, found on first use. It implements go-oidc's
// KeySet.
type lazyKeys struct {
	issuer string
	algs   []string
	now    func() time.Time
	client *http.Client

	mu          sync.Mutex
	keys        gooidc.KeySet
	lastAttempt time.Time
	lastErr     error
}

// newLazyKeys returns the key set of an entry: jwksURL's when set, else the
// one discovered from issuerURL.
func newLazyKeys(issuerURL, jwksURL string, algs []string, now func() time.Time) *lazyKeys {
	k := &lazyKeys{issuer: issuerURL, algs: algs, now: now, client: &http.Client{Timeout: discoveryTimeout}}
	if jwksURL != "" {
		k.keys = issuer.NewKeySet(jwksURL, algs, now)
	}
	return k
}

// VerifySignature finds the key set, then checks raw with it. A discovery
// that fails marks the context as a key outage (issuer.TrackKeyOutage).
func (k *lazyKeys) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	keys, err := k.keySet(ctx)
	if err != nil {
		issuer.MarkKeyOutage(ctx)
		return nil, err
	}
	return keys.VerifySignature(ctx, raw)
}

// keySet returns the key set, discovering it when it is not known yet and
// the last failed attempt is older than the cooldown.
func (k *lazyKeys) keySet(ctx context.Context) (gooidc.KeySet, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys != nil {
		return k.keys, nil
	}
	if !k.lastAttempt.IsZero() && k.now().Sub(k.lastAttempt) < discoveryCooldown {
		return nil, fmt.Errorf("discovery of %s failed less than %s ago: %w", k.issuer, discoveryCooldown, k.lastErr)
	}
	k.lastAttempt = k.now()
	jwksURL, err := k.discover(context.WithoutCancel(ctx))
	k.lastErr = err
	if err != nil {
		return nil, err
	}
	k.keys = issuer.NewKeySet(jwksURL, k.algs, k.now)
	return k.keys, nil
}

// discover reads the issuer's OpenID discovery document and returns its
// jwks_uri. The document must name the same issuer, and the URI must be one
// urlAllowed accepts.
func (k *lazyKeys) discover(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()
	u := strings.TrimSuffix(k.issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("building the discovery request: %w", err)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching %s: %w", u, err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only fetch; a close error changes nothing
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetching %s: status %d", u, resp.StatusCode)
	}
	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryBytes))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", u, err)
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("decoding %s: %w", u, err)
	}
	if doc.Issuer != k.issuer {
		return "", fmt.Errorf("%s names issuer %q, not %q", u, doc.Issuer, k.issuer)
	}
	if !urlAllowed(doc.JWKSURI) {
		return "", fmt.Errorf("%s names jwks_uri %q, which is not https", u, doc.JWKSURI)
	}
	return doc.JWKSURI, nil
}
