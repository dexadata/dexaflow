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

// TenantSchemaURL is the location a tenant schema is added to its compiler
// under. A bare name such as "param.json" is resolved against the control
// plane's working directory, so every compile and validation error that names
// the schema, or a relative "$ref" inside it, would reveal that directory to
// the tenant. Under this non-file base a relative "$ref" resolves to another
// dexaflow:// URL, which noRemoteRefs refuses like any other.
func TenantSchemaURL(name string) string { return "dexaflow://tenant/" + name }

// NewTenantSchemaCompiler returns a JSON Schema compiler for a schema a tenant
// wrote, such as a DAG param schema. Add the schema under TenantSchemaURL so
// errors never name a local path. It resolves references into the schema
// itself (its "$defs") and the standard draft meta-schemas the library ships,
// and refuses every other "$ref": no local file and no remote URL is loaded.
func NewTenantSchemaCompiler() *jsonschema.Compiler {
	c := jsonschema.NewCompiler()
	c.UseLoader(noRemoteRefs{})
	return c
}
