package oidc

import (
	"net/http"
	"time"
)

// httpTimeout bounds every outbound call this package makes to the IdP.
//
// go-oidc and oauth2 both fall back to http.DefaultClient, which has NO timeout,
// so an IdP that accepts the TCP connection and then never answers blocks the
// caller forever. That is three separate paths, and they fail in two different
// ways:
//
//   - Discovery, on the boot path. Already bounded by a context deadline in
//     cmd/dexaflow-server, which is what #1153 was filed for.
//   - The JWKS fetch, on every login whose signing key is not cached. NOT
//     bounded: Provider.Verifier builds its key set over context.Background(),
//     so a request-scoped deadline never reaches it. The client does, because
//     the key set inherits the provider's client (go-oidc oidc.go, remoteKeySet),
//     which is the whole reason injecting one here works at all.
//   - The code exchange, on every login. NOT bounded, and it reads its client
//     from the context under oauth2's own key rather than go-oidc's.
//
// A deadline on the request context does not fix the second one and the server
// sets no WriteTimeout, so before this a hung IdP held one goroutine per login
// attempt until the client gave up. A client timeout is the bound that applies
// to all three whatever the caller passes.
//
// 15s matches the discovery deadline in cmd/dexaflow-server so the two cannot
// drift into disagreeing about how patient this deployment is.
const httpTimeout = 15 * time.Second

// HTTPClient is the client every IdP call in this package uses. A new one per
// call is deliberate over a package-level singleton: these are boot-time and
// per-login calls, the allocation is irrelevant next to the round trip, and a
// shared mutable client is a thing tests reach into.
func HTTPClient() *http.Client { return &http.Client{Timeout: httpTimeout} }
