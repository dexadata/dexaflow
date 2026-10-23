package xcom

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaCacheSize bounds how many compiled schemas are kept. Schemas come from
// user DAGs, so the cache evicts the least recently used one when full.
const schemaCacheSize = 256

// schemaCacheBytes bounds the total size of the schemas kept, measured as the
// length of their canonical JSON: a count bound alone lets 256 large schemas
// hold any amount of memory (#1613). The compiled form grows with the schema
// text, so the text length is the cost the budget counts. 8 MiB is 256 schemas
// of 32 KiB, far above what a DAG declares for its XComs.
const schemaCacheBytes = 8 << 20

// defaultSchemaCache is shared by every Service in the process. Compiled
// schemas hold no tenant data: the key is a hash of the schema's own canonical
// JSON.
var defaultSchemaCache = newSchemaCache(schemaCacheSize, schemaCacheBytes)

// validateSchema checks a JSON payload against a declared JSON Schema, returning
// an error describing the first violation.
func validateSchema(value []byte, schema map[string]any) error {
	return defaultSchemaCache.validate(value, schema)
}

// schemaCache keeps compiled schemas keyed by the SHA-256 of their canonical
// JSON encoding (encoding/json sorts map keys), so equal schemas share one
// compiled schema whatever map instance carries them, and the key does not
// keep a copy of the schema text. Schemas that fail to compile are never
// stored, and neither is one larger than the whole byte budget. When either
// bound is reached, the least recently used schema is evicted; the scan is
// linear but only runs on a miss.
type schemaCache struct {
	mu       sync.Mutex
	max      int
	maxBytes int
	bytes    int
	clock    uint64
	entries  map[[sha256.Size]byte]*schemaCacheEntry
	compiles int
}

type schemaCacheEntry struct {
	compiled *jsonschema.Schema
	size     int
	lastUsed uint64
}

func newSchemaCache(maxEntries, maxBytes int) *schemaCache {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &schemaCache{max: maxEntries, maxBytes: maxBytes, entries: make(map[[sha256.Size]byte]*schemaCacheEntry)}
}

func (c *schemaCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// size is the total length of the schemas held.
func (c *schemaCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
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
	key := sha256.Sum256(raw)
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
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
	if e, ok := c.entries[key]; ok {
		e.lastUsed = c.clock
		return e.compiled, nil
	}
	if len(raw) > c.maxBytes {
		return compiled, nil
	}
	for len(c.entries) >= c.max || c.bytes+len(raw) > c.maxBytes {
		c.evictOldestLocked()
	}
	c.entries[key] = &schemaCacheEntry{compiled: compiled, size: len(raw), lastUsed: c.clock}
	c.bytes += len(raw)
	return compiled, nil
}

func (c *schemaCache) evictOldestLocked() {
	var oldestKey [sha256.Size]byte
	var oldest uint64
	first := true
	for k, e := range c.entries {
		if first || e.lastUsed < oldest {
			oldestKey, oldest, first = k, e.lastUsed, false
		}
	}
	c.bytes -= c.entries[oldestKey].size
	delete(c.entries, oldestKey)
}

func compileSchema(raw []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parsing schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	c.UseLoader(noRemoteRefs{})
	if aerr := c.AddResource(schemaURL, doc); aerr != nil {
		return nil, fmt.Errorf("loading schema: %w", aerr)
	}
	compiled, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}
	return compiled, nil
}
