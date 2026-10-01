package storage

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/config"
)

// Redis wraps a go-redis client used for XCom and locks.
type Redis struct {
	Client *redis.Client
}

// redisOptions parses a redis URL into client options. When caPath is
// non-empty, the PEM CA bundle at that path is loaded and installed as the
// TLSConfig.RootCAs the client uses to verify the server cert — required to
// reach managed Redis (Memorystore SERVER_AUTHENTICATION, ElastiCache
// in-transit, Azure Cache) whose server cert is signed by a per-instance CA
// that is not in the container's system roots (#312). Empty caPath leaves
// the TLSConfig untouched (system roots, same as before the knob existed),
// so non-TLS and non-managed-TLS installs behave identically.
func redisOptions(url, caPath string) (*redis.Options, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parsing redis url: %w", err)
	}
	if caPath == "" {
		return opts, nil
	}
	pem, err := os.ReadFile(caPath) //nolint:gosec // operator-configured path; reading it is the entire point.
	if err != nil {
		return nil, fmt.Errorf("reading redis CA file %q: %w", caPath, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("redis CA file %q has no PEM certificate (expected an `ca.crt` bundle)", caPath)
	}
	// ParseURL initializes TLSConfig only for `rediss://`; an operator who
	// sets a CA against a plaintext `redis://` URL almost certainly wants
	// TLS, so install a fresh config rather than dropping the CA on the
	// floor.
	if opts.TLSConfig == nil {
		opts.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	opts.TLSConfig.RootCAs = pool
	return opts, nil
}

// NewRedis connects to Redis and verifies connectivity.
func NewRedis(ctx context.Context, cfg config.RedisSection) (*Redis, error) {
	opts, err := redisOptions(cfg.URL, cfg.CAFile)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	if perr := client.Ping(ctx).Err(); perr != nil {
		return nil, errors.Join(fmt.Errorf("pinging redis: %w", perr), client.Close())
	}
	return &Redis{Client: client}, nil
}

// Ping checks Redis connectivity (used by /readyz).
func (r *Redis) Ping(ctx context.Context) error {
	return r.Client.Ping(ctx).Err()
}

// Close releases the Redis client.
func (r *Redis) Close() error {
	return r.Client.Close()
}
