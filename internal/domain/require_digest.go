package domain

import (
	"fmt"
)

// DigestRequirement is deploy.require_digest (#1703): what registering a
// version, or making one current again, does with an image that is not
// pinned by a full sha256 digest. A
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
// Before #1703 the compile and push path registered a tag; push --image
// pins an image by hand.
const pinHint = "`dexaflow deploy`, and `dexaflow compile --build --push` followed by `dexaflow push`, " +
	"register the image by digest; to pin it by hand, run `dexaflow push --image <repository>@sha256:<digest>`"

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

// currentHint is the way out for a version that cannot be made current
// again: it is already registered, so pinning a new register does not help.
const currentHint = "Redeploy its sources with `dexaflow deploy`, which registers the image by digest, " +
	"or set deploy.require_digest=warn to make it current"

// CheckCurrentImage is CheckImage for making a registered version current
// again (a rollback): the same decision, with a way out that fits a version
// already registered.
func (r DigestRequirement) CheckCurrentImage(image string) (warning string, err error) {
	if !r.Active() || IsDigestPinned(image) {
		return "", nil
	}
	if r == DigestRequireEnforce {
		return "", fmt.Errorf("this version's image %q is not pinned by digest (<repository>@sha256:<64 hex>), "+
			"and this server refuses to make it current (deploy.require_digest=enforce). %s", image, currentHint)
	}
	return fmt.Sprintf("this version's image %q is not pinned by digest (<repository>@sha256:<64 hex>); "+
		"it is made current under deploy.require_digest=warn, and refused once the server enforces it. "+
		"Redeploy its sources with `dexaflow deploy` to register it by digest", image), nil
}
