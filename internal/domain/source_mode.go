package domain

import "fmt"

// MaxSourceModeBytes caps the dag.py a source-mode version carries (ADR 0067
// §3). The executor ships the source in a pod annotation, and Kubernetes caps
// all of a pod's annotations together at 256 KiB, so half of that leaves room
// for the annotations the executor and operators already set. A constant, not
// a knob.
const MaxSourceModeBytes = 128 << 10

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
	if n := len(spec.Source); n > MaxSourceModeBytes {
		return fmt.Errorf("source mode: DAG %q source is %d bytes, over the %d byte limit; build an image instead", spec.DagID, n, MaxSourceModeBytes)
	}
	return nil
}
