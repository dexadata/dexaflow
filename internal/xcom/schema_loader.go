package xcom

import "errors"

// errRemoteRef is why a schema "$ref" outside the schema itself is refused.
var errRemoteRef = errors.New("references outside the schema are not allowed")

// noRemoteRefs is the URL loader of a tenant-authored schema: it loads nothing.
// The compiler's default loader reads local files, so a "$ref" to a file:// URL,
// or a relative one that resolves against the server's working directory,
// would make the control plane read its own files and echo them back in the
// validation error. A schema can still "$ref" into itself (its "$defs") and
// name a standard draft in "$schema": the compiler ships those meta-schemas and
// never asks the loader for them.
type noRemoteRefs struct{}

// Load refuses every URL.
func (noRemoteRefs) Load(string) (any, error) { return nil, errRemoteRef }
