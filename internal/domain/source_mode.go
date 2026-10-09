package domain

import (
	"fmt"
	"regexp"
)

// MaxSourceModeBytes caps the dag.py a source-mode version carries (ADR 0067
// §3). The executor ships the source in a pod annotation, and Kubernetes caps
// all of a pod's annotations together at 256 KiB, so half of that leaves room
// for the annotations the executor and operators already set. A constant, not
// a knob.
const MaxSourceModeBytes = 128 << 10

// digestPinned matches an image reference that ends in a full sha256 digest.
var digestPinned = regexp.MustCompile(`^[^@]+@sha256:[a-f0-9]{64}$`)

// IsDigestPinned reports whether image is pinned by a full sha256 digest:
// a name, then "@sha256:" and exactly 64 lowercase hex characters at the end.
// The source-mode runtime image must be (ADR 0067 §3).
func IsDigestPinned(image string) bool {
	return digestPinned.MatchString(image)
}

// SourceModeApplies reports whether a version runs in source mode (ADR 0067
// §3): the mode is on (modeImage, the operator's runtime image, is set), the
// version's image is that runtime image, and it carries a source.
func SourceModeApplies(modeImage, image string, hasSource bool) bool {
	return modeImage != "" && image == modeImage && hasSource
}

// CheckSourceMode refuses a version on the runtime image whose source is empty
// or over MaxSourceModeBytes while source mode is on. Any other version, and
// every version with the mode off, passes unchanged.
func CheckSourceMode(modeImage string, spec *DAGSpec) error {
	if modeImage == "" || spec.Image != modeImage {
		return nil
	}
	if spec.Source == "" {
		return fmt.Errorf("source mode: DAG %q uses the runtime image but has no source", spec.DagID)
	}
	return CheckSourceModeSize(spec.DagID, spec.Source)
}

// CheckSourceModeSize refuses a source-mode source over MaxSourceModeBytes.
// Register calls it through CheckSourceMode; dispatch calls it again for a
// version registered before source mode was turned on, which register never
// checked (ADR 0067 §3). Both give the same message.
func CheckSourceModeSize(dagID, source string) error {
	if n := len(source); n > MaxSourceModeBytes {
		return fmt.Errorf("source mode: DAG %q source is %d bytes, over the %d byte limit; build an image instead", dagID, n, MaxSourceModeBytes)
	}
	return nil
}
