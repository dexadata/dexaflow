package xcom

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaCacheSize bounds how many compiled schemas are kept. Schemas come from
// user DAGs, so the cache evicts the least recently used one when full.
const schemaCacheSize = 256

// defaultSchemaCache is shared by every Service in the process. Compiled
// schemas hold no tenant data: the key is the schema's own canonical JSON.
var defaultSchemaCache = newSchemaCache(schemaCacheSize)

// validateSchema checks a JSON payload against a declared JSON Schema, returning
// an error describing the first violation.
func validateSchema(value []byte, schema map[string]any) error {
	return defaultSchemaCache.validate(value, schema)
}

// schemaCache keeps compiled schemas keyed by their canonical JSON encoding
// (encoding/json sorts map keys), so equal schemas share one compiled schema
// whatever map instance carries them. Schemas that fail to compile are never
// stored. When full, the least recently used schema is evicted; the scan is
// linear but only runs on a miss.
type schemaCache struct {
	mu       sync.Mutex
	max      int
	clock    uint64
	entries  map[string]*schemaCacheEntry
	compiles int
}

type schemaCacheEntry struct {
	compiled *jsonschema.Schema
	lastUsed uint64
}

func newSchemaCache(maxEntries int) *schemaCache {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &schemaCache{max: maxEntries, entries: make(map[string]*schemaCacheEntry)}
}

func (c *schemaCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *schemaCache) validate(value []byte, schema map[string]any) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("encoding schema: %w", err)
	}
	compiled, err := c.get(raw)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(value))
	if err != nil {
		return fmt.Errorf("payload is not valid JSON: %w", err)
	}
	return compiled.Validate(inst)
}

// get returns the compiled schema for raw, compiling it on a miss. Compiling
// happens outside the lock; two concurrent misses for the same schema may both
// compile, and the first stored result wins.
func (c *schemaCache) get(raw []byte) (*jsonschema.Schema, error) {
	c.mu.Lock()
	if e, ok := c.entries[string(raw)]; ok {
		c.clock++
		e.lastUsed = c.clock
		c.mu.Unlock()
		return e.compiled, nil
	}
	c.mu.Unlock()

	compiled, err := compileSchema(raw)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.compiles++
	c.clock++
	if e, ok := c.entries[string(raw)]; ok {
		e.lastUsed = c.clock
		return e.compiled, nil
	}
	for len(c.entries) >= c.max {
		c.evictOldestLocked()
	}
	c.entries[string(raw)] = &schemaCacheEntry{compiled: compiled, lastUsed: c.clock}
	return compiled, nil
}

func (c *schemaCache) evictOldestLocked() {
	var oldestKey string
	var oldest uint64
	first := true
	for k, e := range c.entries {
		if first || e.lastUsed < oldest {
			oldestKey, oldest, first = k, e.lastUsed, false
		}
	}
	delete(c.entries, oldestKey)
}

func compileSchema(raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parsing schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if aerr := c.AddResource("xcom_schema.json", doc); aerr != nil {
		return nil, fmt.Errorf("loading schema: %w", aerr)
	}
	compiled, err := c.Compile("xcom_schema.json")
	if err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}
	return compiled, nil
}
