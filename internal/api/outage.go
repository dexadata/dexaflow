package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

// outageRetryAfterSeconds is the Retry-After, in seconds, on every 503 the API
// gives because a dependency it needs to check a credential could not be
// reached (#1100). It is fixed and small on purpose: the server cannot know
// when the store comes back, and the point is only that a client has a number
// to back off on instead of retrying at once, which during an outage turns
// every client into load on the dependency that is already down.
const outageRetryAfterSeconds = 5

// authUnavailable reports whether err, returned while checking a credential,
// means the credential could not be checked rather than that it was judged and
// rejected. A rejection is auth.ErrInvalidToken (alone or joined with a cause);
// any other error is a store or key source that could not be reached (#1087,
// #1097). It is the one rule the data plane, the renew route and the SPA shell
// gate share, so they never disagree about what an outage is.
func authUnavailable(err error) bool {
	return err != nil && !errors.Is(err, auth.ErrInvalidToken)
}

// setOutageRetryAfter adds the outage Retry-After to the response.
func setOutageRetryAfter(c *gin.Context) {
	c.Header("Retry-After", strconv.Itoa(outageRetryAfterSeconds))
}

// abortAuthUnavailable answers 503 "authentication temporarily unavailable"
// with Retry-After: the credential could not be checked, which says nothing
// about the caller (#843, #1087). cause goes to the log only, never to the
// body; nil logs the detail alone.
func abortAuthUnavailable(c *gin.Context, cause error) {
	setOutageRetryAfter(c)
	AbortProblemCause(c, http.StatusServiceUnavailable, "service unavailable", "authentication temporarily unavailable", cause)
}

// outagePage is what a browser gets instead of the SPA shell when its session
// could not be checked because the store was unreachable (#1099). It is static
// on purpose: no script, no asset of the app, nothing from the request and
// nothing about the cause, so it reveals nothing and works with every
// dependency down.
const outagePage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Dexaflow is temporarily unavailable</title>
<style>
 body{font-family:system-ui,sans-serif;background:#0f172a;color:#e2e8f0;display:flex;
   min-height:100vh;align-items:center;justify-content:center;margin:0}
 main{background:#1e293b;padding:2rem;border-radius:12px;max-width:420px;box-shadow:0 10px 30px rgba(0,0,0,.4)}
 h1{font-size:1.25rem;margin:0 0 1rem}
 p{color:#cbd5e1;font-size:.9rem;line-height:1.45;margin:.5rem 0 0}
</style></head><body>
<main>
 <h1>Dexaflow is temporarily unavailable</h1>
 <p>Dexaflow could not check your session because a service it depends on is
 not responding. This is an outage on the server side, not a problem with how
 you signed in.</p>
 <p>Reload this page in a few seconds to try again. If it keeps happening,
 tell whoever administers this Dexaflow.</p>
</main>
</body></html>
`

// serveOutagePage answers a SPA route whose session could not be checked:
// 503 with Retry-After, never cached, never framed, and with no script or
// resource the page could load. The session cookie is left as it is, since
// the session may well be valid once the store answers.
func serveOutagePage(c *gin.Context) {
	setOutageRetryAfter(c)
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
	c.Set(contextKeyProblemDetail, "authentication temporarily unavailable")
	c.Data(http.StatusServiceUnavailable, "text/html; charset=utf-8", []byte(outagePage))
	c.Abort()
}
