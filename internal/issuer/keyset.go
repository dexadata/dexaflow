package issuer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

// KeyRefreshCooldown is the least time between two downloads of the issuer's
// JWKS. A token whose key is not cached triggers a download, and anyone can
// mint such a token, so without a floor every forged request would cost an
// outbound call to the issuer. A real key rotation is picked up at most this
// long after the previous download.
const KeyRefreshCooldown = 30 * time.Second

// keyFetchTimeout bounds one JWKS download, and maxJWKSBytes its body.
const (
	keyFetchTimeout = 10 * time.Second
	maxJWKSBytes    = 1 << 20
)

// ErrKeysUnavailable is a token that could not be checked because the
// issuer's JWKS could not be downloaded. It says nothing about the token, so
// the API answers 503 rather than "sign in again".
var ErrKeysUnavailable = errors.New("issuer: signing keys unavailable")

// keyOutage carries, through the context go-oidc hands to the key set, the
// fact that verification failed because the keys could not be downloaded:
// go-oidc formats the key set's error with %v, which drops the sentinel.
type keyOutage struct{ hit atomic.Bool }

type keyOutageKey struct{}

func withKeyOutage(ctx context.Context) (context.Context, *keyOutage) {
	o := &keyOutage{}
	return context.WithValue(ctx, keyOutageKey{}, o), o
}

func markKeyOutage(ctx context.Context) {
	if o, ok := ctx.Value(keyOutageKey{}).(*keyOutage); ok {
		o.hit.Store(true)
	}
}

// cachedKeySet is the issuer's JWKS, cached and downloaded again only when a
// token names a key it does not hold, and then at most once per cooldown. It
// implements go-oidc's KeySet.
type cachedKeySet struct {
	url      string
	client   *http.Client
	algs     []jose.SignatureAlgorithm
	now      func() time.Time
	cooldown time.Duration

	// fetch serializes downloads, so a burst on a cold cache waits for one.
	// It is a one-slot semaphore rather than a mutex so a waiting caller can
	// give up when its own context ends (#1588).
	fetch chan struct{}

	mu          sync.Mutex // guards the fields below
	keys        []jose.JSONWebKey
	gen         uint64 // bumped on every successful download
	lastAttempt time.Time
	lastErr     error
}

func newCachedKeySet(url string, algs []string, now func() time.Time) *cachedKeySet {
	ks := &cachedKeySet{
		url:      url,
		client:   &http.Client{Timeout: keyFetchTimeout},
		now:      now,
		cooldown: KeyRefreshCooldown,
		fetch:    make(chan struct{}, 1),
	}
	for _, a := range algs {
		ks.algs = append(ks.algs, jose.SignatureAlgorithm(a))
	}
	return ks
}

// VerifySignature checks raw's signature against the cached keys, and
// downloads the JWKS again when none verifies and the cooldown allows it.
func (ks *cachedKeySet) VerifySignature(ctx context.Context, raw string) ([]byte, error) {
	jws, err := jose.ParseSigned(raw, ks.algs)
	if err != nil {
		return nil, fmt.Errorf("malformed jwt: %w", err)
	}
	if len(jws.Signatures) != 1 {
		return nil, errors.New("want exactly one signature")
	}
	ks.mu.Lock()
	keys, gen := ks.keys, ks.gen
	ks.mu.Unlock()
	if payload, ok := verifyWith(jws, keys); ok {
		return payload, nil
	}
	keys, err = ks.refresh(ctx, gen)
	if err != nil {
		if errors.Is(err, ErrKeysUnavailable) {
			markKeyOutage(ctx)
		}
		return nil, err
	}
	if payload, ok := verifyWith(jws, keys); ok {
		return payload, nil
	}
	return nil, errors.New("no published key verifies the signature")
}

// verifyWith tries the keys that match the signature's key id (all of them
// when it names none).
func verifyWith(jws *jose.JSONWebSignature, keys []jose.JSONWebKey) ([]byte, bool) {
	kid := jws.Signatures[0].Header.KeyID
	for i := range keys {
		if kid != "" && keys[i].KeyID != kid {
			continue
		}
		if payload, err := jws.Verify(&keys[i]); err == nil {
			return payload, true
		}
	}
	return nil, false
}

// refresh downloads the JWKS unless another caller already did since seen,
// or the last attempt is younger than the cooldown. Within the cooldown it
// refuses: as an outage when the last attempt failed, as an unknown key
// otherwise. A caller whose context ends while another caller's download runs
// stops waiting and gets an outage: its token was not checked.
func (ks *cachedKeySet) refresh(ctx context.Context, seen uint64) ([]jose.JSONWebKey, error) {
	select {
	case ks.fetch <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: waiting for the key download: %w", ErrKeysUnavailable, ctx.Err())
	}
	defer func() { <-ks.fetch }()

	ks.mu.Lock()
	if ks.gen != seen {
		keys := ks.keys
		ks.mu.Unlock()
		return keys, nil
	}
	if !ks.lastAttempt.IsZero() && ks.now().Sub(ks.lastAttempt) < ks.cooldown {
		lastErr := ks.lastErr
		ks.mu.Unlock()
		if lastErr != nil {
			return nil, fmt.Errorf("%w: last download failed: %w", ErrKeysUnavailable, lastErr)
		}
		return nil, errors.New("unknown signing key, and the keys were downloaded less than the cooldown ago")
	}
	ks.lastAttempt = ks.now()
	ks.mu.Unlock()

	// A caller that goes away must not abort the download for everyone, nor
	// record a failure the issuer did not cause.
	keys, err := ks.download(context.WithoutCancel(ctx))

	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.lastErr = err
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeysUnavailable, err)
	}
	ks.keys = keys
	ks.gen++
	return keys, nil
}

func (ks *cachedKeySet) download(ctx context.Context) ([]jose.JSONWebKey, error) {
	ctx, cancel := context.WithTimeout(ctx, keyFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ks.url, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("building jwks request: %w", err)
	}
	resp, err := ks.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only fetch; a close error changes nothing
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching jwks: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKSBytes))
	if err != nil {
		return nil, fmt.Errorf("reading jwks: %w", err)
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return nil, fmt.Errorf("decoding jwks: %w", err)
	}
	return set.Keys, nil
}

// NewKeySet returns the JWKS published at url as a go-oidc key set, with
// the cache, the refresh cooldown and the size and time bounds the trusted
// issuer uses. It makes no network call until a signature is checked. Only
// algs are accepted. The CI OIDC issuers (ADR 0092) share it.
func NewKeySet(url string, algs []string, now func() time.Time) gooidc.KeySet {
	return newCachedKeySet(url, algs, now)
}

// TrackKeyOutage returns a context to verify a token with, and a function
// that reports, after the verification, whether it failed because the keys
// could not be downloaded rather than because the token is bad.
func TrackKeyOutage(ctx context.Context) (tracked context.Context, outage func() bool) {
	tracked, o := withKeyOutage(ctx)
	return tracked, o.hit.Load
}

// MarkKeyOutage records on a context from TrackKeyOutage that the keys could
// not be obtained, for a key set that finds its keys some other way first
// (an OpenID discovery document, say).
func MarkKeyOutage(ctx context.Context) {
	markKeyOutage(ctx)
}
