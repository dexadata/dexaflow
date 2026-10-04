package xcom

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// validateSchema checks a JSON payload against a declared JSON Schema, returning
// an error describing the first violation.
func validateSchema(value []byte, schema map[string]any) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encoding schema: %w", err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("parsing schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noRemoteRefs{})
	if aerr := c.AddResource("xcom_schema.json", doc); aerr != nil {
		return fmt.Errorf("loading schema: %w", aerr)
	}
	compiled, err := c.Compile("xcom_schema.json")
	if err != nil {
		return fmt.Errorf("compiling schema: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(value))
	if err != nil {
		return fmt.Errorf("payload is not valid JSON: %w", err)
	}
	return compiled.Validate(inst)
}
