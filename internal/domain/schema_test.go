package domain

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedSchemasMatchDocs guards against drift between the schemas
// embedded in this package and the canonical sources under docs/api, which
// are the single source of truth.
func TestEmbeddedSchemasMatchDocs(t *testing.T) {
	cases := map[string][]byte{
		"dag-schema.json":          dagSchemaJSON,
		"leoflow-yaml-schema.json": leoflowSchemaJSON,
	}
	for file, embedded := range cases {
		t.Run(file, func(t *testing.T) {
			canonical, err := os.ReadFile(filepath.Join("..", "..", "docs", "api", file))
			if err != nil {
				t.Fatalf("reading canonical schema: %v", err)
			}
			if !bytes.Equal(embedded, canonical) {
				t.Errorf("embedded %s differs from docs/api/%s; re-copy the canonical schema", file, file)
			}
		})
	}
}
