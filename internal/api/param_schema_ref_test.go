package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateParamValueRefusesFileRef: the trigger-time check compiles the
// stored param schema again, so it must refuse a local file "$ref" too and
// never quote the file in the 400 it returns to the caller.
func TestValidateParamValueRefusesFileRef(t *testing.T) {
	const marker = "LOCAL-FILE-CONTENT-41c8"
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
			err := validateParamValue(json.RawMessage(`{"$ref":"`+ref+`"}`), json.RawMessage(`"x"`))
			if err == nil {
				t.Fatal("a param schema with a local file $ref validated a value")
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("validation error echoes the local file: %v", err)
			}
			if name == "relative" && strings.Contains(err.Error(), dir) {
				t.Fatalf("validation error reveals the server working directory: %v", err)
			}
		})
	}
}

// TestValidateParamValueHidesWorkingDirectory pins #1402: a plain schema
// violation names the schema by its location, which used to be resolved
// against the control plane's working directory and so revealed it to the
// caller in the 400.
func TestValidateParamValueHidesWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	err := validateParamValue(json.RawMessage(`{"type":"integer"}`), json.RawMessage(`"x"`))
	if err == nil {
		t.Fatal("a string validated against an integer schema")
	}
	if strings.Contains(err.Error(), dir) {
		t.Fatalf("validation error reveals the server working directory: %v", err)
	}
}
