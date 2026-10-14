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

// UserExistsFunc reports whether a user with the given email exists in the
// tenant the caller is bootstrapping.
type UserExistsFunc func(ctx context.Context, email string) (bool, error)

// ResolveAdminEmail returns the email the bootstrap admin lives under.
//
// An empty configured email means DefaultAdminEmail. Any address other than
// DefaultAdminEmail is returned unchanged, with no lookup: an operator who named
// an address gets exactly that address. DefaultAdminEmail resolves to
// LegacyAdminEmail when the legacy admin exists and the new one does not, so an
// install created before the rename (or a reinstall over its kept database)
// keeps the admin it has instead of gaining a second one. The existing user is
// never renamed.
func ResolveAdminEmail(ctx context.Context, configured string, exists UserExistsFunc) (string, error) {
	if configured == "" {
		configured = DefaultAdminEmail
	}
	if configured != DefaultAdminEmail {
		return configured, nil
	}
	found, err := exists(ctx, DefaultAdminEmail)
	if err != nil {
		return "", fmt.Errorf("looking up the bootstrap admin: %w", err)
	}
	if found {
		return DefaultAdminEmail, nil
	}
	legacy, err := exists(ctx, LegacyAdminEmail)
	if err != nil {
		return "", fmt.Errorf("looking up the legacy bootstrap admin: %w", err)
	}
	if legacy {
		return LegacyAdminEmail, nil
	}
	return DefaultAdminEmail, nil
}
