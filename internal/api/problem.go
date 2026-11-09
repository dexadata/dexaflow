// Package api implements the Airflow-compatible HTTP control plane (ADR 0007).
package api

import "github.com/gin-gonic/gin"

// Problem is an RFC 7807 problem-details response body.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// AbortProblemCause writes an RFC 7807 problem whose detail is safe to show the
// caller, while handing the real error to the request log.
//
// It exists because the two audiences need different text. A storage failure
// carries the driver's own message, for Postgres, "severity: message (SQLSTATE
// code)" with our constraint, table and column names inside, and a tenant of a
// multi-tenant control plane must not receive that (CWE-209). The operator, on
// the other hand, cannot diagnose the failure without it. Redacting the
// response and dropping the cause would trade one defect for another, so cause
// is logged verbatim under its own field.
//
// cause may be nil, which makes this identical to AbortProblem.
func AbortProblemCause(c *gin.Context, status int, title, detail string, cause error) {
	if cause != nil {
		c.Set(contextKeyProblemCause, cause.Error())
	}
	AbortProblem(c, status, title, detail)
}

// AbortProblem writes an RFC 7807 problem response and stops the handler chain.
//
// detail is sent to the client as-is, so it must never carry a driver,
// filesystem, or network error's text. Where the real cause must stay
// server-side, use AbortProblemCause.
func AbortProblem(c *gin.Context, status int, title, detail string) {
	// Record the cause so StructuredLogger can log WHY this failed (4xx/5xx are
	// otherwise logged as a bare status code).
	if detail != "" {
		c.Set(contextKeyProblemDetail, detail)
	} else {
		c.Set(contextKeyProblemDetail, title)
	}
	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(status, Problem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: c.Request.URL.Path,
	})
}
