package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateRefusesParamSchemaFileRef: a DAG param schema is tenant-authored
// and compiled on the control plane at registration, so a "$ref" to a local
// file (absolute file:// or relative to the working directory) must not be
// loaded, and the registration error must not quote the file.
func TestValidateRefusesParamSchemaFileRef(t *testing.T) {
	const marker = "LOCAL-FILE-CONTENT-9b21"
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.json"), []byte(`{"const":"`+marker+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for name, ref := range map[string]string{
		"absolute": "file://" + filepath.ToSlash(filepath.Join(dir, "secret.json")),
		"relative": "secret.json",
	} {
		t.Run(name, func(t *testing.T) {
			spec := paramSpecWith("p", ParamSpec{
				Default: json.RawMessage(`"x"`),
				Schema:  json.RawMessage(`{"$ref":"` + ref + `"}`),
			})
			err := spec.Validate()
			if err == nil {
				t.Fatal("a param schema with a local file $ref registered")
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("registration error echoes the local file: %v", err)
			}
		})
	}
}
