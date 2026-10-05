package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	keyFieldSecret   = "secret_key"
	keyFieldPrevious = "secret_key_previous"
)

// liteKeyConfig is what ~/.dexaflow/config.yaml records about encryption keys,
// read from the file alone with no environment overlay.
type liteKeyConfig struct {
	// exists is false when there is no config file.
	exists bool
	// secretKey is the encrypting key; "" means the file records none, which
	// stands for the published constant (a Legacy install).
	secretKey string
	// previous are the decrypt-only predecessors, in order.
	previous []string
}

// readLiteKeyConfig reads the key fields of config.yaml strictly.
//
// configFileSecrets returns empty fields for a file it cannot parse, which is
// right for a best-effort sync and wrong for anything that decides keys: an
// "empty" read of a real key makes the install fall back to the published
// constant, or makes a rewrite persist the emptiness (ADR 0065 gap 8). So here
// a file that exists but cannot be read EXACTLY is an error: unparseable YAML,
// a document that is not a mapping, a key field that is not a scalar, a key
// field given twice, or a key spelled in another case (the loader matches keys
// case-insensitively, so it would read a value this reader did not).
func readLiteKeyConfig(path string) (liteKeyConfig, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-owned path under the user's home
	if errors.Is(err, os.ErrNotExist) {
		return liteKeyConfig{}, nil
	}
	if err != nil {
		return liteKeyConfig{}, fmt.Errorf("reading %s: %w", path, err)
	}
	root, err := parseConfigDoc(raw)
	if err != nil {
		return liteKeyConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	out := liteKeyConfig{exists: true}
	key, err := scalarField(root, keyFieldSecret)
	if err != nil {
		return liteKeyConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	prev, err := scalarField(root, keyFieldPrevious)
	if err != nil {
		return liteKeyConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	out.secretKey = strings.TrimSpace(key)
	out.previous = splitKeyList(prev)
	return out, nil
}

// splitKeyList splits a comma-separated key list, dropping empty entries.
func splitKeyList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// parseConfigDoc parses config.yaml into its top-level mapping node.
func parseConfigDoc(raw []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("not valid YAML: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("not a YAML mapping of settings; run `dexaflow setup` to write one")
	}
	root := doc.Content[0]
	if err := checkKeyFields(root); err != nil {
		return nil, err
	}
	return root, nil
}

// checkKeyFields refuses duplicate or differently-cased key fields.
func checkKeyFields(root *yaml.Node) error {
	seen := map[string]bool{}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k := root.Content[i].Value
		lower := strings.ToLower(k)
		if lower != keyFieldSecret && lower != keyFieldPrevious {
			continue
		}
		if k != lower {
			return fmt.Errorf("%q must be spelled %q", k, lower)
		}
		if seen[k] {
			return fmt.Errorf("%q is set more than once", k)
		}
		seen[k] = true
		if root.Content[i+1].Kind != yaml.ScalarNode {
			return fmt.Errorf("%q must be a single string", k)
		}
	}
	return nil
}

// scalarField returns the value of a top-level scalar field, "" when absent.
func scalarField(root *yaml.Node, name string) (string, error) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != name {
			continue
		}
		v := root.Content[i+1]
		if v.Tag == "!!null" {
			return "", nil
		}
		return v.Value, nil
	}
	return "", nil
}

// setLiteKeys returns raw with secret_key set to key and secret_key_previous
// set to the comma-joined previous list, or removed when previous is empty.
// Every other field, comment and its order is carried over: the document is
// edited, not regenerated (writeLiteConfig rewrites a fixed set of fields and
// would drop anything else, gap 8). Values are written double-quoted so that a
// key YAML would read as a number, a boolean or null reads back as the exact
// string written.
func setLiteKeys(raw []byte, key string, previous []string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("not valid YAML: %w", err)
	}
	if _, err := parseConfigDoc(raw); err != nil {
		return nil, err
	}
	root := doc.Content[0]
	setScalar(root, keyFieldSecret, key)
	if len(previous) == 0 {
		removeField(root, keyFieldPrevious)
	} else {
		setScalar(root, keyFieldPrevious, strings.Join(previous, ","))
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encoding config: %w", err)
	}
	return buf.Bytes(), nil
}

func setScalar(root *yaml.Node, name, value string) {
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value, Style: yaml.DoubleQuotedStyle}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == name {
			v.LineComment = root.Content[i+1].LineComment
			root.Content[i+1] = v
			return
		}
	}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name}, v)
}

func removeField(root *yaml.Node, name string) {
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == name {
			root.Content = append(root.Content[:i], root.Content[i+2:]...)
			return
		}
	}
}
