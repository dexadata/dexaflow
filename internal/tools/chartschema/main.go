// Command chartschema regenerates helm/dexaflow/values.schema.json from the
// chart's values.yaml, so `helm install`, `helm upgrade` and `helm template`
// refuse a misspelled or misplaced value instead of ignoring it (#2320).
//
// The schema follows values.yaml:
//
//   - a mapping with keys accepts those keys and refuses any other;
//   - an empty mapping ({}) accepts any key: it is a map the operator fills;
//   - a list accepts any items;
//   - a leaf accepts the type of its default, the string spelling of that type
//     (a quoted "true" or "30" in a values file), and null. A string default,
//     empty or not, accepts any scalar: "" often stands for "unset, use the
//     profile's value" and an image tag such as 1.2 parses as a number.
//   - a null default accepts anything.
//
// overrides.yaml, next to this file, names the exceptions: objects handed to
// Kubernetes as written (open) and values that accept a form their default
// does not show (types). Every override must name a value values.yaml has.
//
// Usage, from the repository root:
//
//	go run ./internal/tools/chartschema          # rewrite the schema
//	go run ./internal/tools/chartschema -check   # fail when the schema is stale
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Paths relative to the repository root.
const (
	valuesPath    = "helm/dexaflow/values.yaml"
	schemaPath    = "helm/dexaflow/values.schema.json"
	overridesPath = "internal/tools/chartschema/overrides.yaml"
)

// Patterns for the string spelling of a typed default.
const (
	boolPattern   = "^(true|false)$"
	intPattern    = "^-?[0-9]+$"
	numberPattern = "^-?[0-9]+(\\.[0-9]+)?$"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail instead of writing when the schema is stale")
	flag.Parse()
	if err := run(*root, *check); err != nil {
		fmt.Fprintln(os.Stderr, "chartschema:", err)
		os.Exit(1)
	}
}

func run(root string, check bool) error {
	want, err := generateFromRoot(root)
	if err != nil {
		return err
	}
	path := filepath.Join(root, schemaPath)
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading schema: %w", err)
	}
	if bytes.Equal(current, want) {
		return nil
	}
	if check {
		return fmt.Errorf("%s is stale: run `make gen-chart-schema` and commit the result", schemaPath)
	}
	if err := os.WriteFile(path, want, 0o644); err != nil { //nolint:gosec // a chart file, world-readable like the rest of the tree
		return fmt.Errorf("writing schema: %w", err)
	}
	return nil
}

// generateFromRoot reads values.yaml and overrides.yaml under root and
// returns the schema bytes.
func generateFromRoot(root string) ([]byte, error) {
	values, err := os.ReadFile(filepath.Join(root, valuesPath))
	if err != nil {
		return nil, fmt.Errorf("reading values: %w", err)
	}
	overrides, err := os.ReadFile(filepath.Join(root, overridesPath))
	if err != nil {
		return nil, fmt.Errorf("reading overrides: %w", err)
	}
	return generate(values, overrides)
}

// overrideFile is overrides.yaml.
type overrideFile struct {
	Open  []string            `yaml:"open"`
	Types map[string][]string `yaml:"types"`
}

// node is one schema object; fields are written in a fixed order.
type node struct {
	Schema               string   `json:"$schema,omitempty"`
	Title                string   `json:"title,omitempty"`
	Type                 []string `json:"type,omitempty"`
	Pattern              string   `json:"pattern,omitempty"`
	Properties           *props   `json:"properties,omitempty"`
	AdditionalProperties *bool    `json:"additionalProperties,omitempty"` //nolint:tagliatelle // JSON Schema keyword
}

// props keeps properties in values.yaml order.
type props struct {
	keys  []string
	nodes map[string]*node
}

func (p *props) add(k string, n *node) {
	if p.nodes == nil {
		p.nodes = map[string]*node{}
	}
	p.keys = append(p.keys, k)
	p.nodes[k] = n
}

// MarshalJSON writes the properties in insertion order.
func (p *props) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range p.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		val, err := json.Marshal(p.nodes[k])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// generator carries the overrides and records which ones were used.
type generator struct {
	open  map[string]bool
	types map[string][]string
	used  map[string]bool
}

// generate returns the schema for one values document.
func generate(values, overrides []byte) ([]byte, error) {
	var ov overrideFile
	if len(bytes.TrimSpace(overrides)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(overrides))
		dec.KnownFields(true)
		if err := dec.Decode(&ov); err != nil {
			return nil, fmt.Errorf("decoding overrides: %w", err)
		}
	}
	g := &generator{open: map[string]bool{}, types: ov.Types, used: map[string]bool{}}
	for _, p := range ov.Open {
		g.open[p] = true
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(values, &doc); err != nil {
		return nil, fmt.Errorf("decoding values: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("values: the document must be a mapping")
	}
	root, err := g.walk("", doc.Content[0])
	if err != nil {
		return nil, err
	}
	root.Schema = "http://json-schema.org/draft-07/schema#"
	root.Title = "Values of the dexaflow chart (generated by internal/tools/chartschema; do not edit)"
	root.Type = []string{"object"}
	// Helm passes a global section to every chart's values.
	root.Properties.add("global", &node{Type: []string{"object", "null"}})
	if uerr := g.unused(); uerr != nil {
		return nil, uerr
	}
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding schema: %w", err)
	}
	return append(out, '\n'), nil
}

// unused fails on an override that names no value.
func (g *generator) unused() error {
	var dead []string
	for p := range g.open {
		if !g.used["open:"+p] {
			dead = append(dead, "open "+p)
		}
	}
	for p := range g.types {
		if !g.used["types:"+p] {
			dead = append(dead, "types "+p)
		}
	}
	if len(dead) == 0 {
		return nil
	}
	sort.Strings(dead)
	return fmt.Errorf("overrides name no value of values.yaml (or open names a leaf): %s", strings.Join(dead, ", "))
}

func join(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// walk returns the schema of the value at path.
func (g *generator) walk(path string, v *yaml.Node) (*node, error) {
	if t, ok := g.types[path]; ok {
		g.used["types:"+path] = true
		return &node{Type: append(append([]string{}, t...), "null")}, nil
	}
	switch v.Kind {
	case yaml.MappingNode:
		return g.mapping(path, v)
	case yaml.SequenceNode:
		return &node{Type: []string{"array", "null"}}, nil
	case yaml.ScalarNode:
		return leaf(v), nil
	case yaml.AliasNode:
		return g.walk(path, v.Alias)
	default:
		return nil, fmt.Errorf("%s: unsupported YAML node kind %d", path, v.Kind)
	}
}

func (g *generator) mapping(path string, v *yaml.Node) (*node, error) {
	if g.open[path] {
		g.used["open:"+path] = true
		return &node{Type: []string{"object", "null"}}, nil
	}
	if len(v.Content) == 0 {
		return &node{Type: []string{"object", "null"}}, nil
	}
	strict := false
	n := &node{Type: []string{"object", "null"}, Properties: &props{}, AdditionalProperties: &strict}
	for i := 0; i+1 < len(v.Content); i += 2 {
		key := v.Content[i].Value
		child, err := g.walk(join(path, key), v.Content[i+1])
		if err != nil {
			return nil, err
		}
		n.Properties.add(key, child)
	}
	return n, nil
}

// leaf maps a scalar default to the forms the value accepts.
func leaf(v *yaml.Node) *node {
	switch v.Tag {
	case "!!null":
		return &node{}
	case "!!bool":
		return &node{Type: []string{"boolean", "string", "null"}, Pattern: boolPattern}
	case "!!int":
		return &node{Type: []string{"integer", "string", "null"}, Pattern: intPattern}
	case "!!float":
		return &node{Type: []string{"number", "string", "null"}, Pattern: numberPattern}
	default:
		return &node{Type: []string{"string", "number", "integer", "boolean", "null"}}
	}
}
