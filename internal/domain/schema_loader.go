package domain

import (
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// errRemoteRef is why a schema "$ref" outside the schema itself is refused.
var errRemoteRef = errors.New("references outside the schema are not allowed")

// noRemoteRefs is the URL loader of a tenant-authored schema: it loads nothing.
// The compiler's default loader reads local files, so a "$ref" to a file:// URL,
// or a relative one that resolves against the server's working directory,
// would make the control plane read its own files and quote them in a
// validation error.
type noRemoteRefs struct{}

// Load refuses every URL.
func (noRemoteRefs) Load(string) (any, error) { return nil, errRemoteRef }

// NewTenantSchemaCompiler returns a JSON Schema compiler for a schema a tenant
// wrote, such as a DAG param schema. It resolves references into the schema
// itself (its "$defs") and the standard draft meta-schemas the library ships,
// and refuses every other "$ref": no local file and no remote URL is loaded.
func NewTenantSchemaCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.UseLoader(noRemoteRefs{})
	return c
}
