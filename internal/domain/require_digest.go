package domain

import (
	"fmt"
)

// DigestRequirement is deploy.require_digest (#1703): what registering a
// version does with an image that is not pinned by a full sha256 digest. A
// tag can name other bytes tomorrow, and nodes that pulled it at different
// times can run different code for one version.
type DigestRequirement string

// The values of deploy.require_digest.
const (
	// DigestRequireOff accepts any image, as every release before the setting
	// did. The default.
	DigestRequireOff DigestRequirement = "off"
	// DigestRequireWarn accepts an unpinned image with a warning in the
	// register's answer, a log line and a count, so an operator can find the
	// pipelines to fix before enforcing.
	DigestRequireWarn DigestRequirement = "warn"
	// DigestRequireEnforce refuses an unpinned image at register.
	DigestRequireEnforce DigestRequirement = "enforce"
)

// pinHint is how to pin an image, shared by the warning and the refusal.
const pinHint = "`dexaflow deploy` registers the pushed image by digest; " +
	"after `dexaflow compile --build --push`, set image in dag.json to <repository>@sha256:<digest> before `dexaflow push`"

// ParseDigestRequirement reads a configured deploy.require_digest: "" is
// DigestRequireOff, and any value other than off, warn or enforce is an
// error naming the key.
func ParseDigestRequirement(s string) (DigestRequirement, error) {
	switch r := DigestRequirement(s); r {
	case "":
		return DigestRequireOff, nil
	case DigestRequireOff, DigestRequireWarn, DigestRequireEnforce:
		return r, nil
	}
	return "", fmt.Errorf("deploy.require_digest %q must be %q, %q or %q",
		s, DigestRequireOff, DigestRequireWarn, DigestRequireEnforce)
}

// Active reports whether r checks images at all (warn or enforce).
func (r DigestRequirement) Active() bool {
	return r == DigestRequireWarn || r == DigestRequireEnforce
}

// CheckImage applies r to the image of a version being registered. A pinned
// image (IsDigestPinned), and any image while r is off, returns nothing.
// Under warn an unpinned image returns the warning to give; under enforce it
// returns the refusal, which is the client's to fix.
func (r DigestRequirement) CheckImage(image string) (warning string, err error) {
	if !r.Active() || IsDigestPinned(image) {
		return "", nil
	}
	if r == DigestRequireEnforce {
		return "", fmt.Errorf("image %q is not pinned by digest (<repository>@sha256:<64 hex>), "+
			"and this server refuses it (deploy.require_digest=enforce). %s", image, pinHint)
	}
	return fmt.Sprintf("image %q is not pinned by digest (<repository>@sha256:<64 hex>); "+
		"it is accepted under deploy.require_digest=warn, and refused once the server enforces it. %s", image, pinHint), nil
}
