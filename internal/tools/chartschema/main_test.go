package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the repository root seen from this package's directory.
const repoRoot = "../../.."

// TestSchemaIsCurrent fails when the committed values.schema.json differs from
// what the generator writes, which is how a value added to values.yaml without
// regenerating the schema is caught in CI (`go test ./...`).
func TestSchemaIsCurrent(t *testing.T) {
	committed, err := os.ReadFile(filepath.Join(repoRoot, schemaPath))
	if err != nil {
		t.Fatal(err)
	}
	want, err := generateFromRoot(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, committed) {
		t.Fatalf("%s is stale: run `make gen-chart-schema` and commit the result", schemaPath)
	}
}

// TestCheckFailsOnStaleSchema drives the -check entry point against a copy of
// the chart whose values.yaml gained a key the schema does not list.
func TestCheckFailsOnStaleSchema(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{valuesPath, schemaPath, overridesPath} {
		data, err := os.ReadFile(filepath.Join(repoRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, rel), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(root, true); err != nil {
		t.Fatalf("check on an unchanged copy: %v", err)
	}
	values := filepath.Join(root, valuesPath)
	data, err := os.ReadFile(values)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(values, append(data, []byte("\n# -- Added without regenerating.\nnewKey: 1\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	err = run(root, true)
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("check on a stale schema: got %v, want a stale error", err)
	}
}

// schemaOf generates the schema of one values document and decodes it.
func schemaOf(t *testing.T, values, overrides string) map[string]any {
	t.Helper()
	out, err := generate([]byte(values), []byte(overrides))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatalf("schema is not JSON: %v\n%s", err, out)
	}
	return s
}

// prop walks properties along a dotted path.
func prop(t *testing.T, s map[string]any, path string) map[string]any {
	t.Helper()
	cur := s
	for _, k := range strings.Split(path, ".") {
		props, ok := cur["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: no properties above %q", path, k)
		}
		next, ok := props[k].(map[string]any)
		if !ok {
			t.Fatalf("%s: no property %q", path, k)
		}
		cur = next
	}
	return cur
}

func types(t *testing.T, node map[string]any) string {
	t.Helper()
	raw, ok := node["type"]
	if !ok {
		return ""
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("type is not a list: %v", raw)
	}
	parts := make([]string, 0, len(list))
	for _, v := range list {
		parts = append(parts, v.(string))
	}
	return strings.Join(parts, ",")
}

// TestLeafTypesFollowTheDefault locks how a default maps to the forms a value
// accepts: the default's own type, its string spelling, and null.
func TestLeafTypesFollowTheDefault(t *testing.T) {
	s := schemaOf(t, `
flag: false
count: 3
ratio: 0.5
name: abc
unset: ""
anything: null
list: []
`, "")
	cases := map[string]string{
		"flag":  "boolean,string,null",
		"count": "integer,string,null",
		"ratio": "number,string,null",
		"name":  "string,number,integer,boolean,null",
		"unset": "string,number,integer,boolean,null",
		"list":  "array,null",
	}
	for path, want := range cases {
		if got := types(t, prop(t, s, path)); got != want {
			t.Errorf("%s: type %q, want %q", path, got, want)
		}
	}
	if got := types(t, prop(t, s, "anything")); got != "" {
		t.Errorf("anything: a null default must accept every type, got %q", got)
	}
	if got := prop(t, s, "flag")["pattern"]; got != "^(true|false)$" {
		t.Errorf("flag: pattern %v", got)
	}
	if got := prop(t, s, "count")["pattern"]; got != "^-?[0-9]+$" {
		t.Errorf("count: pattern %v", got)
	}
}

// TestObjectsRefuseUnknownKeys locks the point of the schema: a section with
// listed keys refuses any other, an empty map or a section named open in the
// overrides accepts any key, and the top level accepts Helm's global.
func TestObjectsRefuseUnknownKeys(t *testing.T) {
	s := schemaOf(t, `
section:
  known: 1
  nested:
    deep: ""
labels: {}
resources:
  limits:
    cpu: "1"
`, "open:\n  - resources\n")
	if s["additionalProperties"] != false {
		t.Errorf("top level: additionalProperties %v, want false", s["additionalProperties"])
	}
	if _, ok := s["properties"].(map[string]any)["global"]; !ok {
		t.Errorf("top level: global must be accepted")
	}
	for _, path := range []string{"section", "section.nested"} {
		if got := prop(t, s, path)["additionalProperties"]; got != false {
			t.Errorf("%s: additionalProperties %v, want false", path, got)
		}
	}
	for _, path := range []string{"labels", "resources"} {
		node := prop(t, s, path)
		if _, strict := node["additionalProperties"]; strict {
			t.Errorf("%s: must accept any key, got additionalProperties %v", path, node["additionalProperties"])
		}
		if _, listed := node["properties"]; listed {
			t.Errorf("%s: an open object lists no properties", path)
		}
	}
}

// TestTypeOverrides lets a value accept forms its default does not show.
func TestTypeOverrides(t *testing.T) {
	s := schemaOf(t, "pdb:\n  minAvailable: 1\n", "types:\n  pdb.minAvailable: [integer, string]\n")
	if got := types(t, prop(t, s, "pdb.minAvailable")); got != "integer,string,null" {
		t.Errorf("pdb.minAvailable: type %q", got)
	}
	if _, ok := prop(t, s, "pdb.minAvailable")["pattern"]; ok {
		t.Errorf("an override replaces the default's pattern")
	}
}

// TestOverridesMustNameValues refuses an override for a path values.yaml does
// not have, so a renamed value cannot leave a dead override behind.
func TestOverridesMustNameValues(t *testing.T) {
	for _, overrides := range []string{
		"open:\n  - missing\n",
		"types:\n  a.missing: [string]\n",
		"open:\n  - a.b\n",
	} {
		if _, err := generate([]byte("a:\n  b: 1\n"), []byte(overrides)); err == nil {
			t.Errorf("overrides %q: want an error", overrides)
		}
	}
}
