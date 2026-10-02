package domain

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed schemas/dag-schema.json
var dagSchemaJSON []byte

//go:embed schemas/leoflow-yaml-schema.json
var leoflowSchemaJSON []byte

// compiledSchemas holds the parsed JSON Schemas used to validate domain types.
type compiledSchemas struct {
	dag     *jsonschema.Schema
	leoflow *jsonschema.Schema
}

// schemas compiles the embedded schemas exactly once, on first use.
var schemas = sync.OnceValues(loadSchemas)

func loadSchemas() (compiledSchemas, error) {
	dag, err := compileSchema("dag.json", dagSchemaJSON)
	if err != nil {
		return compiledSchemas{}, fmt.Errorf("compiling dag schema: %w", err)
	}
	leoflow, err := compileSchema("dexaflow.yaml", leoflowSchemaJSON)
	if err != nil {
		return compiledSchemas{}, fmt.Errorf("compiling project schema: %w", err)
	}
	return compiledSchemas{dag: dag, leoflow: leoflow}, nil
}

// validateParamSpec refuses a declared DAG param whose JSON Schema does not
// compile, or whose non-null default violates its own schema — at registration,
// while the author is still looking, instead of failing every trigger forever. A
// null or absent default is not checked (it means "no default / required").
func validateParamSpec(name string, schema, def []byte) error {
	if len(schema) == 0 || string(schema) == "{}" {
		return nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return fmt.Errorf("param %q: parsing schema: %w", name, err)
	}
	c := jsonschema.NewCompiler()
	if aerr := c.AddResource("param.json", doc); aerr != nil {
		return fmt.Errorf("param %q: loading schema: %w", name, aerr)
	}
	compiled, cerr := c.Compile("param.json")
	if cerr != nil {
		return fmt.Errorf("param %q: invalid schema: %w", name, cerr)
	}
	if len(def) == 0 || string(def) == "null" {
		return nil
	}
	inst, ierr := jsonschema.UnmarshalJSON(bytes.NewReader(def))
	if ierr != nil {
		return fmt.Errorf("param %q: default is not valid JSON: %w", name, ierr)
	}
	if verr := compiled.Validate(inst); verr != nil {
		return fmt.Errorf("param %q: default value violates its schema: %w", name, verr)
	}
	return nil
}

func compileSchema(name string, raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(name, doc); err != nil {
		return nil, err
	}
	return c.Compile(name)
}

// validateAgainst marshals v to JSON and validates it against the schema,
// returning the aggregated schema violations (or nil when v conforms).
func validateAgainst(sch *jsonschema.Schema, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshaling for validation: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("decoding for validation: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("schema validation: %w", err)
	}
	return nil
}
