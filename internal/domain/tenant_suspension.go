package domain

import "time"

// SuspensionMode says how much of a suspended tenant's work stops (#2181).
type SuspensionMode string

// SuspensionHold creates no new run for the tenant, manual or scheduled, and
// lets the work already queued or running finish. It is the only mode the
// engine applies today; the schema reserves "drain" and "stop" for later.
const SuspensionHold SuspensionMode = "hold"

// MaxSuspensionReasonLength bounds the reason an operator gives, in bytes. It
// is shown back in refusals and logs, so it stays short.
const MaxSuspensionReasonLength = 500

// TenantSuspension is a tenant's suspension, set by the operator through the
// service API. The zero value is a tenant that is not suspended. The tenant's
// DAGs keep their own paused flag, so lifting a suspension restores exactly
// what the tenant had.
type TenantSuspension struct {
	Mode   SuspensionMode
	Reason string
	// Since is when the tenant was first suspended; changing the reason of a
	// suspension in place keeps it.
	Since time.Time
}

// Active reports whether the tenant is suspended.
func (s TenantSuspension) Active() bool { return s.Mode != "" }

// Validate refuses a suspension the engine cannot apply: a mode other than
// hold, or a reason longer than MaxSuspensionReasonLength.
func (s TenantSuspension) Validate() error {
	if s.Mode != SuspensionHold {
		return Safef(ErrValidation, "suspension mode %q is not supported: use %q (drain and stop are not supported yet)", s.Mode, SuspensionHold)
	}
	if len(s.Reason) > MaxSuspensionReasonLength {
		return Safef(ErrValidation, "suspension reason is %d bytes, above the limit of %d", len(s.Reason), MaxSuspensionReasonLength)
	}
	return nil
}

// RefusalError is the error a write that would create work for the suspended
// tenant returns: ErrTenantSuspended, with the reason when there is one.
func (s TenantSuspension) RefusalError() error {
	if s.Reason == "" {
		return Safef(ErrTenantSuspended, "the tenant is suspended")
	}
	return Safef(ErrTenantSuspended, "the tenant is suspended: %s", s.Reason)
}
