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

// ErrDatabaseReplaced is returned when the database is reachable but is not
// the one this release was installed on: it carries another installation id,
// none, or no schema at all (ADR 0090 decision 3). The health endpoints name
// it apart from ErrSchemaNotCurrent, because migrating does not fix it: an
// operator restores the right database, or accepts this one.
var ErrDatabaseReplaced = errors.New("database replaced")

// ErrLimitExceeded is returned when a write would take a tenant past one of the
// limits the operator set for it (TenantLimits). The API maps it to 403: the
// request is well formed, and repeating it will not succeed until the limit or
// the tenant's usage changes.
var ErrLimitExceeded = errors.New("tenant limit exceeded")

// ErrTenantSuspended is returned when a write would create work for a tenant
// the operator suspended through the service API. The API maps it to 409: the
// request is well formed, and it succeeds once the suspension is lifted.
var ErrTenantSuspended = errors.New("tenant suspended")

// ErrValueUnreadable is returned when a stored value is encrypted and no
// configured secret key opens it. It is a server configuration fault, not the
// caller's: the API answers 500, with a message that says what happened.
var ErrValueUnreadable = errors.New("stored value cannot be decrypted")
