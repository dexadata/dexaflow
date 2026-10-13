package cli

import (
	"fmt"
	"strings"
)

// labelOrigin records where a DAG version label came from, so a 409 on
// register can say which way out applies.
type labelOrigin int

const (
	// labelUnknown is a dag.json compiled elsewhere (`dexaflow push`).
	labelUnknown labelOrigin = iota
	// labelExplicit is a label the caller passed with --dag-version.
	labelExplicit
	// labelDefault is the content-derived default label (defaultDAGVersion).
	labelDefault
)

const conflictPreamble = "hint: version %q of this DAG is already registered with different content, and versions are immutable.\n"

// versionConflictHint turns the bare 409 the control plane answers when a
// version label is reused with different content into the way out. Versions
// are immutable and an identical spec is a no-op, so a 409 always means the
// same label with something different in it.
//
// With the default label, derived from the DAG's inputs, the hashed inputs
// cannot differ; what differs is the image, rebuilt to a new digest, or a file
// the hash does not cover. With an explicit
// --dag-version, the label was reused. Every other error passes through.
func versionConflictHint(err error, label string, origin labelOrigin) error {
	if err == nil || !strings.Contains(err.Error(), "server returned 409") {
		return err
	}
	var why string
	switch origin {
	case labelExplicit:
		why = "      The label came from --dag-version: pass a new --dag-version or omit it, so the label is derived from the DAG's content."
	case labelDefault:
		why = "      The default label is derived from the DAG's inputs, so the hashed inputs are unchanged: either the image was rebuilt to a new digest,\n" +
			"      or the DAG reads files outside its source and include_paths (add them to include_paths). To register anyway, pass a new --dag-version."
	default:
		why = "      If the label came from --dag-version, compile again with a new --dag-version or omit it, so the label is derived from the DAG's content.\n" +
			"      With the default label the sources are unchanged and the image was rebuilt to a new digest: the registered version already runs these sources."
	}
	return fmt.Errorf("%w\n"+conflictPreamble+"%s", err, label, why)
}
