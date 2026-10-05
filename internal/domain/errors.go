package domain

import "errors"

// ErrNotFound is returned when a requested resource does not exist.
var ErrNotFound = errors.New("resource not found")

// ErrConflict is returned when a write conflicts with an existing resource (e.g.
// a duplicate dag run for the same logical date). The API maps it to 409.
var ErrConflict = errors.New("resource already exists")

// ErrValidation is returned when input fails a business-rule check that the
// caller can fix (e.g. creating a user with a role that does not exist). The API
// maps it to 400.
var ErrValidation = errors.New("invalid input")

// ErrSchemaNotCurrent is returned when the database is reachable but its
// migration state is not one the running binary can serve. It lives here, next
// to the other cross-cutting sentinels, because two layers need to agree on it:
// storage produces the verdict, and the health endpoints must tell it apart
// from "the database could not be read at all". Both answers are a 503, but one
// sends an operator to the migration Job and the other to the connection pool,
// and a probe that conflates them costs an on-call hour.
var ErrSchemaNotCurrent = errors.New("database schema is not current")

// ErrLimitExceeded is returned when a write would take a tenant past one of the
// limits the operator set for it (TenantLimits). The API maps it to 403: the
// request is well formed, and repeating it will not succeed until the limit or
// the tenant's usage changes.
var ErrLimitExceeded = errors.New("tenant limit exceeded")
