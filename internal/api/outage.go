package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// outageRetryAfterSeconds is the Retry-After, in seconds, on every 503 the API
// gives because a dependency it needs to check a credential could not be
// reached (#1100). It is fixed and small on purpose: the server cannot know
// when the store comes back, and the point is only that a client has a number
// to back off on instead of retrying at once, which during an outage turns
// every client into load on the dependency that is already down.
const outageRetryAfterSeconds = 5

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
