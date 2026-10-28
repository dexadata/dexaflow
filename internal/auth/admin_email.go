package auth

import (
	"context"
	"fmt"
)

// DefaultAdminEmail is the email of the bootstrap admin a new install creates:
// a fresh Lite setup, and a server booted with a bootstrap password and no
// bootstrap email.
const DefaultAdminEmail = "admin@dexaflow.local"

// LegacyAdminEmail is the bootstrap admin email of installs created before the
// rename to Dexaflow. Those installs keep their admin under this address; see
// ResolveAdminEmail.
const LegacyAdminEmail = "admin@leoflow.local"

// AdminState is what the user store knows about one email address.
type AdminState int

const (
	// UserAbsent means no user has the address.
	UserAbsent AdminState = iota
	// UserWithoutAdminRole means a user has the address but not the admin role.
	UserWithoutAdminRole
	// UserIsAdmin means a user has the address and holds the admin role.
	UserIsAdmin
)

// AdminLookup reports the AdminState of an email address in the tenant the
// caller is bootstrapping.
type AdminLookup func(ctx context.Context, email string) (AdminState, error)

// AdminResolution is the address ResolveAdminEmail chose and why.
type AdminResolution struct {
	// Email is the address the bootstrap admin lives under.
	Email string
	// KeptLegacy reports that Email is LegacyAdminEmail, chosen in place of the
	// default address because the install predates the rename.
	KeptLegacy bool
	// BothExist reports that users exist under both addresses, which the
	// caller should warn about: only Email is bootstrapped.
	BothExist bool
}

// ResolveAdminEmail returns the address the bootstrap admin lives under.
//
// An empty configured email means DefaultAdminEmail. Any address other than
// DefaultAdminEmail is returned unchanged, with no lookup: an operator who
// named an address gets exactly that address. For DefaultAdminEmail the user
// holding the admin role wins, the new address first, so an install created
// before the rename (or a reinstall over its kept database) keeps the admin it
// has instead of gaining a second one, and the bootstrap never writes a
// password to a non-admin account at the new address while the legacy admin
// exists. Without an admin at either address, an existing user wins, the new
// address first; with no user at all, the new address is created. The existing
// user is never renamed.
func ResolveAdminEmail(ctx context.Context, configured string, lookup AdminLookup) (AdminResolution, error) {
	if configured == "" {
		configured = DefaultAdminEmail
	}
	if configured != DefaultAdminEmail {
		return AdminResolution{Email: configured}, nil
	}
	cur, err := lookup(ctx, DefaultAdminEmail)
	if err != nil {
		return AdminResolution{}, fmt.Errorf("looking up the bootstrap admin: %w", err)
	}
	legacy, err := lookup(ctx, LegacyAdminEmail)
	if err != nil {
		return AdminResolution{}, fmt.Errorf("looking up the legacy bootstrap admin: %w", err)
	}
	both := cur != UserAbsent && legacy != UserAbsent
	keepLegacy := legacy == UserIsAdmin && cur != UserIsAdmin ||
		legacy == UserWithoutAdminRole && cur == UserAbsent
	if keepLegacy {
		return AdminResolution{Email: LegacyAdminEmail, KeptLegacy: true, BothExist: both}, nil
	}
	return AdminResolution{Email: DefaultAdminEmail, BothExist: both}, nil
}
