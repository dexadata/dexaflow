package xcom

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// secretMarker is the content of a local file a tenant schema must never reach.
const secretMarker = "LOCAL-FILE-CONTENT-7f3a"

// writeSecretSchema writes a schema file that only accepts the marker, so a
// payload that is not the marker fails with an error quoting it, and returns
// its directory.
func writeSecretSchema(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "secret.json"), []byte(`{"const":"`+secretMarker+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestPushSchemaRefusesLocalFileRefs pins the fix for a tenant-authored
// xcom_schema reading the control plane's local files: a "$ref" to a file://
// URL, or a relative one that resolves against the working directory, must not
// be loaded. Before, the compiler's default loader read the file and the
// validation error echoed its content back to the task.
func TestPushSchemaRefusesLocalFileRefs(t *testing.T) {
	dir := writeSecretSchema(t)
	fileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, "secret.json"))}).String()
	t.Chdir(dir)
	for name, ref := range map[string]string{"absolute": fileURL, "relative": "secret.json"} {
		t.Run(name, func(t *testing.T) {
			s := newService(newFakeBackend(), &fakeIndex{})
			err := s.Push(context.Background(), testKey(), []byte(`"anything"`), "application/json", map[string]any{"$ref": ref})
			if !errors.Is(err, ErrSchemaMismatch) {
				t.Fatalf("Push with a %s file $ref = %v, want ErrSchemaMismatch", name, err)
			}
			if strings.Contains(err.Error(), secretMarker) {
				t.Fatalf("Push error echoes the local file: %v", err)
			}
			if name == "relative" && strings.Contains(err.Error(), dir) {
				t.Fatalf("Push error reveals the server working directory: %v", err)
			}
		})
	}
}

// TestPushSchemaRefusesRemoteRefs: no URL outside the schema itself is
// fetched, whatever its scheme.
func TestPushSchemaRefusesRemoteRefs(t *testing.T) {
	s := newService(newFakeBackend(), &fakeIndex{})
	for _, ref := range []string{"http://169.254.169.254/latest/meta-data", "https://example.com/schema.json"} {
		if err := s.Push(context.Background(), testKey(), []byte(`1`), "application/json", map[string]any{"$ref": ref}); !errors.Is(err, ErrSchemaMismatch) {
			t.Errorf("Push with $ref %q = %v, want ErrSchemaMismatch", ref, err)
		}
	}
}

// TestPushSchemaKeepsLocalRefsAndMetaSchemas: what a self-contained schema
// needs still works: a "$ref" into its own "$defs" and a "$schema" naming a
// standard draft, whose meta-schema ships with the compiler.
func TestPushSchemaKeepsLocalRefsAndMetaSchemas(t *testing.T) {
	schema := map[string]any{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs":   map[string]any{"rows": map[string]any{"type": "integer"}},
		"type":    "object",
		"properties": map[string]any{
			"rows": map[string]any{"$ref": "#/$defs/rows"},
		},
	}
	s := newService(newFakeBackend(), &fakeIndex{})
	if err := s.Push(context.Background(), testKey(), []byte(`{"rows":1}`), "application/json", schema); err != nil {
		t.Errorf("conforming payload: %v", err)
	}
	if err := s.Push(context.Background(), testKey(), []byte(`{"rows":"x"}`), "application/json", schema); !errors.Is(err, ErrSchemaMismatch) {
		t.Errorf("violating payload = %v, want ErrSchemaMismatch", err)
	}
}
