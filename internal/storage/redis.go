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

// Redis wraps a go-redis client used for XCom values and the live log tail:
// a single node, a Sentinel-managed master or a Redis Cluster (#2358).
type Redis struct {
	Client redis.UniversalClient
	// Cluster is set for a Redis Cluster, where a multi-key command must
	// stay within one hash slot.
	Cluster bool
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

// redisUniversalOptions builds the client options from the configuration: a
// single node from the URL, a Sentinel-managed master from redis.sentinel, or
// a Cluster from redis.cluster. With Sentinel or Cluster the URL, when set,
// supplies the credentials, the database and TLS; its host is not used, and
// TLS verifies each node against the address it dials (the master a sentinel
// names, a cluster node), never the URL's host. The explicit timeouts and
// pool size override the URL's and the client's defaults when positive.
func redisUniversalOptions(cfg config.RedisSection) (*redis.UniversalOptions, error) {
	base := &redis.Options{}
	if cfg.URL != "" {
		var err error
		if base, err = redisOptions(cfg.URL, cfg.CAFile); err != nil {
			return nil, err
		}
	} else if cfg.CAFile != "" {
		tlsOpts, err := redisOptions("rediss://localhost", cfg.CAFile)
		if err != nil {
			return nil, err
		}
		base.TLSConfig = tlsOpts.TLSConfig
	}
	topology := cfg.Sentinel.Master != "" || len(cfg.Cluster.Addrs) > 0
	if topology && base.TLSConfig != nil {
		// ParseURL pins ServerName to the URL's host, and go-redis hands one
		// TLS config to every sentinel, master and cluster node. Empty, the
		// TLS dial verifies the host of the address each connection dials.
		base.TLSConfig = base.TLSConfig.Clone()
		base.TLSConfig.ServerName = ""
	}
	u := &redis.UniversalOptions{
		Addrs:        []string{base.Addr},
		DB:           base.DB,
		Username:     base.Username,
		Password:     base.Password,
		Protocol:     base.Protocol,
		ClientName:   base.ClientName,
		TLSConfig:    base.TLSConfig,
		DialTimeout:  base.DialTimeout,
		ReadTimeout:  base.ReadTimeout,
		WriteTimeout: base.WriteTimeout,
		PoolSize:     base.PoolSize,
		MinIdleConns: base.MinIdleConns,
		MaxIdleConns: base.MaxIdleConns,
		MaxRetries:   base.MaxRetries,
	}
	switch {
	case cfg.Sentinel.Master != "":
		u.MasterName = cfg.Sentinel.Master
		u.Addrs = cfg.Sentinel.Addrs
		u.SentinelPassword = cfg.Sentinel.Password
	case len(cfg.Cluster.Addrs) > 0:
		if base.DB != 0 {
			return nil, fmt.Errorf("redis: a Redis Cluster has database 0 only, and the URL names database %d", base.DB)
		}
		u.Addrs = cfg.Cluster.Addrs
		u.IsClusterMode = true
	}
	if cfg.DialTimeout > 0 {
		u.DialTimeout = cfg.DialTimeout
	}
	if cfg.ReadTimeout > 0 {
		u.ReadTimeout = cfg.ReadTimeout
	}
	if cfg.WriteTimeout > 0 {
		u.WriteTimeout = cfg.WriteTimeout
	}
	if cfg.PoolSize > 0 {
		u.PoolSize = cfg.PoolSize
	}
	return u, nil
}

// NewRedis connects to Redis and verifies connectivity.
func NewRedis(ctx context.Context, cfg config.RedisSection) (*Redis, error) {
	opts, err := redisUniversalOptions(cfg)
	if err != nil {
		return nil, err
	}
	client := redis.NewUniversalClient(opts)
	if perr := client.Ping(ctx).Err(); perr != nil {
		return nil, errors.Join(fmt.Errorf("pinging redis: %w", perr), client.Close())
	}
	return &Redis{Client: client, Cluster: opts.IsClusterMode}, nil
}

// Ping checks Redis connectivity (used by /readyz).
func (r *Redis) Ping(ctx context.Context) error {
	return r.Client.Ping(ctx).Err()
}

// Close releases the Redis client.
func (r *Redis) Close() error {
	return r.Client.Close()
}
