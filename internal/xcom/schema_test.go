package xcom

import (
	"fmt"
	"strings"
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
	cache := newSchemaCache(8, schemaCacheBytes)
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
	cache := newSchemaCache(4, schemaCacheBytes)
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

// bigSchema is a schema whose canonical JSON is about n bytes, padded with a
// description, distinct per id.
func bigSchema(id, n int) map[string]any {
	return map[string]any{"type": "object", "title": fmt.Sprintf("s%d", id), "description": strings.Repeat("x", n)}
}

// TestSchemaCacheIsBoundedByBytes pins #1613: a count bound alone lets 256
// large schemas hold an unbounded amount of memory. The cache also keeps the
// schemas it holds under a byte budget, evicting the least recently used.
func TestSchemaCacheIsBoundedByBytes(t *testing.T) {
	cache := newSchemaCache(256, 10_000)
	for i := 0; i < 20; i++ {
		if err := cache.validate([]byte(`{}`), bigSchema(i, 3_000)); err != nil {
			t.Fatalf("validate %d: %v", i, err)
		}
		if b := cache.size(); b > 10_000 {
			t.Fatalf("after %d schemas the cache holds %d bytes, over its 10000 budget", i+1, b)
		}
	}
	if n := cache.len(); n != 3 {
		t.Errorf("cache holds %d schemas of about 3000 bytes under a 10000 budget, want 3", n)
	}
	// The most recently used schema is the one kept.
	before := cache.compiles
	if err := cache.validate([]byte(`{}`), bigSchema(19, 3_000)); err != nil {
		t.Fatal(err)
	}
	if cache.compiles != before {
		t.Error("the most recently used schema was evicted")
	}
}

// TestSchemaCacheSkipsASchemaOverItsBudget: a schema larger than the whole
// budget still validates, compiled each time, and is never cached.
func TestSchemaCacheSkipsASchemaOverItsBudget(t *testing.T) {
	cache := newSchemaCache(4, 1_000)
	for i := 0; i < 2; i++ {
		if err := cache.validate([]byte(`{}`), bigSchema(0, 2_000)); err != nil {
			t.Fatalf("validate #%d: %v", i, err)
		}
		if err := cache.validate([]byte(`[]`), bigSchema(0, 2_000)); err == nil {
			t.Fatalf("validate #%d accepted an array for an object schema", i)
		}
	}
	if n, b := cache.len(), cache.size(); n != 0 || b != 0 {
		t.Errorf("cache holds %d schemas, %d bytes, want none", n, b)
	}
}

// TestSchemaCacheDoesNotKeepBrokenSchemas: a schema that fails to compile is
// reported every time, never cached as if it were valid.
func TestSchemaCacheDoesNotKeepBrokenSchemas(t *testing.T) {
	cache := newSchemaCache(4, schemaCacheBytes)
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
	cache := newSchemaCache(2, schemaCacheBytes)
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
