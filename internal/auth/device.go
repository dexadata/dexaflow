package auth

import "time"

// DeviceAuthorization is a device sign-in of the CLI waiting for a person to
// approve it (`dexaflow auth login --device`, ADR 0081), as storage returns
// it. Neither of its codes is here: storage keeps only their keyed hashes.
type DeviceAuthorization struct {
	// ID names the pending sign-in between the verification page, the IdP
	// round trip and the consent page.
	ID string
	// CreatedAt is when the CLI started the sign-in.
	CreatedAt time.Time
	// ExpiresAt is when the sign-in can no longer be approved or redeemed.
	ExpiresAt time.Time
}

// DevicePollOutcome is what one poll of a device sign-in found.
type DevicePollOutcome int

// The outcomes of a poll. Each maps to one answer of the token route
// (RFC 8628 section 3.5).
const (
	// DevicePollUnknown is a device code nobody issued, or one already
	// redeemed or refused: invalid_grant.
	DevicePollUnknown DevicePollOutcome = iota
	// DevicePollPending is a sign-in nobody approved yet:
	// authorization_pending.
	DevicePollPending
	// DevicePollSlowDown is a poll that came sooner than the interval: the
	// interval grew by five seconds, slow_down.
	DevicePollSlowDown
	// DevicePollExpired is a sign-in past its expiry: expired_token.
	DevicePollExpired
	// DevicePollDenied is a sign-in the person canceled: access_denied. It
	// is answered once; later polls find nothing.
	DevicePollDenied
	// DevicePollApproved is an approved sign-in, redeemed by this poll: the
	// token is minted for User. Later polls find nothing.
	DevicePollApproved
)

// DevicePoll is the result of one poll: its outcome and, for an approved
// sign-in, the user it was approved as, with the roles the sign-in resolved.
type DevicePoll struct {
	Outcome DevicePollOutcome
	User    User
}
