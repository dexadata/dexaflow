package main

import (
	"time"
)

// The bounds of the boot path that scripts/check-startup-probe-budget.sh adds
// up against the chart's startup probe live in this file, so the gate reads
// one place.

// podInformerSyncTimeout bounds the informer cache warm-up on the boot path. The
// warm cache is an optimization (every consumer falls back to a live read), so
// waiting longer than this buys nothing a running control plane cannot get on its
// own, while waiting unbounded costs the HTTP and metrics listeners entirely
// (#1083). A var so a test can shorten it; nothing changes it at runtime.
var podInformerSyncTimeout = 10 * time.Second

// oidcDiscoveryTimeout bounds the IdP discovery request on the boot path. It is
// a var so tests can shrink it; the value is what ships.
//
// go-oidc discovers over http.DefaultClient, which has no timeout, so an issuer
// that accepts the connection and never answers parks boot before the listener
// binds. 15s is generous for one well-known document over the public internet
// and far below the startup-probe budget that gates the pod
// (scripts/check-startup-probe-budget.sh reconciles the two).
var oidcDiscoveryTimeout = 15 * time.Second

// oidcNameCheckTimeout bounds the boot-time existence lookups, all of them
// together rather than one each. It is a var so tests can shrink it; the value
// is what ships. The lookups are unique-index probes (tenants.name is UNIQUE,
// roles is UNIQUE (tenant_id, name)) against a database whose schema was just
// read, one per distinct tenant plus one per distinct role name, so 5s is
// already generous.
//
// It ADDS to the startup-probe budget rather than sitting inside it: this is a
// further sequential phase of boot, after the Postgres connect retry, not a
// share of it. The retry having succeeded proves the pool connects, which is
// exactly not what this bound guards against: a pool can be established and a
// query still never answer. scripts/check-startup-probe-budget.sh counts it, and
// raising it here without raising probes.startup.failureThreshold there fails
// that gate rather than turning a slow boot into a restart loop.
var oidcNameCheckTimeout = 5 * time.Second
