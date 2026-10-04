package api

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// NoStoreOnVolatileRoutes stamps every response served from the SPA-facing
// JSON surface (`/api/v2/*` and `/ui/*`) with `Cache-Control: no-store,
// must-revalidate` so the browser HTTP cache does not return a pre-mutation
// payload after a PATCH/POST/DELETE.
//
// Why this exists (#211, #271): mark-state PATCH succeeds in single-digit
// ms; TanStack Query then invalidates its in-memory cache and re-fetches.
// Without this header, the browser's HTTP cache layer can serve the OLD
// response to that re-fetch (the original GET response had no explicit
// caching directive, so the browser falls back to heuristic caching). The
// SPA renders stale state until the next "natural" refresh — the
// observable symptom is "marcar como falha demora uma eternidade".
//
// Static assets (`/ide/vs/*` for the Monaco bundle) are content-hashed
// and SHOULD cache, so they are explicitly excluded.
//
// We deliberately use "no-store" rather than "no-cache": no-store forbids
// the browser from writing the response anywhere, which is the strongest
// guarantee we can give a TanStack-backed SPA. "must-revalidate" is added
// for older intermediaries (proxies / SW) that may not honor no-store
// alone. This is ADR-0017-compatible: no SPA changes.
//
// A handler that computes an ETag and authorizes every request may replace the
// header with "private, no-cache" when ui.etag_revalidation is on, so the
// browser revalidates instead of refetching (tiSummariesHandler).
func NoStoreOnVolatileRoutes() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/v2/") || strings.HasPrefix(p, "/ui/") {
			c.Header("Cache-Control", "no-store, must-revalidate")
		}
		c.Next()
	}
}
