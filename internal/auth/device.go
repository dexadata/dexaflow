package auth

import (
	"errors"
	"time"
)

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
	// RequesterAddr is the address that started the sign-in, as the server
	// saw it, shown on the consent page.
	RequesterAddr string
}

// DeviceAuthorizationRequest is what starting a device sign-in stores: the
// keyed hashes of its codes and of the address that started it, that
// address for the consent page, and the limits it starts under.
type DeviceAuthorizationRequest struct {
	DeviceCodeHash string
	UserCodeHash   string
	// RequesterHash is the keyed hash of the starting address (its /56 for
	// IPv6), which MaxLivePerRequester counts by.
	RequesterHash string
	RequesterAddr string
	TTL           time.Duration
	Interval      time.Duration
	// MaxLive caps the sign-ins in progress in all, and
	// MaxLivePerRequester those from one requester.
	MaxLive             int
	MaxLivePerRequester int
}

// The refusals of a start over a cap.
var (
	// ErrDeviceSignInsFull: the cluster holds as many sign-ins in progress
	// as it allows.
	ErrDeviceSignInsFull = errors.New("too many device sign-ins are in progress")
	// ErrDeviceSignInsFromRequester: the starting address holds as many
	// sign-ins in progress as one address may.
	ErrDeviceSignInsFromRequester = errors.New("too many device sign-ins are in progress from this address")
	// ErrDeviceStartsBusy: the start could not take its turn among
	// concurrent starts in time, so it was not recorded.
	ErrDeviceStartsBusy = errors.New("device sign-in starts are busy")
)

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
