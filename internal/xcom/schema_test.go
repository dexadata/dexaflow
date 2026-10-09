package xcom

import (
	"fmt"
	"sync"
	"testing"
)

func rowsSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"required":   []any{"rows"},
		"properties": map[string]any{"rows": map[string]any{"type": "integer"}},
	}
}

// TestValidateSchemaCompilesEachSchemaOnce pins the cache: a task pushes many
// values against the same declared schema, and compiling it on every push was
// the dominant cost of a schema-checked PushXCom. Equal schemas (the same map
// content, not the same map) share one compiled schema.
func TestValidateSchemaCompilesEachSchemaOnce(t *testing.T) {
	cache := newSchemaCache(8)
	for i := 0; i < 5; i++ {
		if err := cache.validate([]byte(`{"rows":1}`), rowsSchema()); err != nil {
			t.Fatalf("validate #%d: %v", i, err)
		}
	}
	if got := cache.compiles; got != 1 {
		t.Errorf("compiled %d times for one schema, want 1", got)
	}
	if err := cache.validate([]byte(`{"rows":"x"}`), rowsSchema()); err == nil {
		t.Error("a cached schema must still reject a violating payload")
	}
}

// TestSchemaCacheIsBounded: schemas come from user DAGs, so the cache must not
// grow with every distinct schema ever seen.
func TestSchemaCacheIsBounded(t *testing.T) {
	cache := newSchemaCache(4)
	for i := 0; i < 20; i++ {
		schema := map[string]any{"type": "object", "title": fmt.Sprintf("s%d", i)}
		if err := cache.validate([]byte(`{}`), schema); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
	}
	if n := cache.len(); n > 4 {
		t.Errorf("cache holds %d schemas, want at most 4", n)
	}
}

// TestSchemaCacheDoesNotKeepBrokenSchemas: a schema that fails to compile is
// reported every time, never cached as if it were valid.
func TestSchemaCacheDoesNotKeepBrokenSchemas(t *testing.T) {
	cache := newSchemaCache(4)
	broken := map[string]any{"type": 12}
	for i := 0; i < 2; i++ {
		if err := cache.validate([]byte(`{}`), broken); err == nil {
			t.Fatalf("validate #%d accepted a broken schema", i)
		}
	}
	if n := cache.len(); n != 0 {
		t.Errorf("cache holds %d schemas after compile errors, want 0", n)
	}
}

// TestSchemaCacheConcurrentUse validates from many goroutines under -race.
func TestSchemaCacheConcurrentUse(t *testing.T) {
	cache := newSchemaCache(2)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				schema := rowsSchema()
				schema["title"] = fmt.Sprintf("s%d", (g+i)%3)
				if err := cache.validate([]byte(`{"rows":1}`), schema); err != nil {
					t.Errorf("validate: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkValidateSchema(b *testing.B) {
	schema := rowsSchema()
	value := []byte(`{"rows":100}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := validateSchema(value, schema); err != nil {
			b.Fatal(err)
		}
	}
}
