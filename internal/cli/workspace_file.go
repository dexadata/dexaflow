package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	yaml "go.yaml.in/yaml/v3"
)

// workspaceFileName marks the root of a workspace: a folder holding many DAG
// projects (under dags/) and the facts every deploy from it shares, the named
// control planes it deploys to and the default registry and build settings.
const workspaceFileName = "dexaflow.workspace.yaml"

// legacyWorkspaceFileName is the workspace file under the pre-rename name. It
// is read when it is the only one present, like leoflow.yaml for a project.
const legacyWorkspaceFileName = "leoflow.workspace.yaml"

// workspaceFileTemplate is what `dexaflow workspace init` writes. Credentials
// never go in this file: it is meant to be committed and shared.
const workspaceFileTemplate = `# Dexaflow workspace: the DAG projects under dags/ and the control planes
# they deploy to. Commit this file; it holds no credentials.
schema_version: "1.0"

# Named control planes. Add one with:
#   dexaflow target add dev https://dexaflow.example.com --registry ghcr.io/acme
# then sign in once with:  dexaflow auth login --target dev
targets: {}

# Shared defaults for every DAG in the workspace. A DAG's own dexaflow.yaml
# wins over them, and a target's registry, platforms and base_image win over
# these when that target is the one deployed to.
# defaults:
#   registry: ghcr.io/acme        # image prefix; the image name defaults to the dag_id
#   platforms: [linux/amd64]
#   base_image: ghcr.io/acme/dexaflow-base:1
`

// deployDefaults are the build and push settings a workspace, or one of its
// targets, supplies to the projects that leave them unset.
type deployDefaults struct {
	// Registry is the image prefix (url) a project without registry.url
	// pushes to. Its image name defaults to the dag_id.
	Registry string `yaml:"registry,omitempty"`
	// Platforms replaces build.platforms for projects that set none.
	Platforms []string `yaml:"platforms,omitempty"`
	// BaseImage replaces base_image for projects that set none.
	BaseImage string `yaml:"base_image,omitempty"`
}

// workspaceTarget is one named control plane in a workspace file.
type workspaceTarget struct {
	// Server is the control plane base URL.
	Server         string `yaml:"server"`
	deployDefaults `yaml:",inline"`
}

// workspaceSpec is the content of a workspace file.
type workspaceSpec struct {
	SchemaVersion string                      `yaml:"schema_version,omitempty"`
	DefaultTarget string                      `yaml:"default_target,omitempty"`
	Targets       map[string]*workspaceTarget `yaml:"targets,omitempty"`
	Defaults      deployDefaults              `yaml:"defaults,omitempty"`
}

// workspaceLocation is a workspace found on disk.
type workspaceLocation struct {
	// Root is the absolute directory holding the workspace file.
	Root string
	// File is the absolute path of the workspace file.
	File string
	// Spec is the parsed file.
	Spec *workspaceSpec
}

// targetNamePattern is what a target name may look like: short, lowercase and
// free of characters that would need quoting in YAML or a shell.
var targetNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// validateTargetName refuses a target name outside targetNamePattern.
func validateTargetName(name string) error {
	if !targetNamePattern.MatchString(name) {
		return fmt.Errorf("invalid target name %q: use lowercase letters, digits, '-' and '_' (at most 63 characters, starting with a letter or digit)", name)
	}
	return nil
}

// validateServerURL refuses anything but an absolute http(s) URL, so a typo is
// reported where it was written rather than as a dial error on every command.
func validateServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid server URL %q: want an absolute http:// or https:// URL", raw)
	}
	return nil
}

// workspaceFileIn returns the workspace file in dir, preferring the current
// name over the legacy one, or "" when dir holds neither.
func workspaceFileIn(dir string) string {
	for _, name := range []string{workspaceFileName, legacyWorkspaceFileName} {
		if p := filepath.Join(dir, name); fileExists(p) {
			return p
		}
	}
	return ""
}

// findWorkspace walks up from start to the first directory holding a
// workspace file and parses it. It returns nil, nil when no directory on the
// way has one; a file that exists but is invalid is an error naming it.
func findWorkspace(start string) (*workspaceLocation, error) {
	dir, err := filepath.Abs(start)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", start, err)
	}
	for {
		if p := workspaceFileIn(dir); p != "" {
			spec, perr := parseWorkspaceFile(p)
			if perr != nil {
				return nil, perr
			}
			return &workspaceLocation{Root: dir, File: p, Spec: spec}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil //nolint:nilnil // no workspace is a normal outcome, not an error.
		}
		dir = parent
	}
}

// findWorkspaceFromCwd is findWorkspace from the process working directory.
func findWorkspaceFromCwd() (*workspaceLocation, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("resolving the current directory: %w", err)
	}
	return findWorkspace(wd)
}

// parseWorkspaceFile reads and validates the workspace file at path. Unknown
// keys are refused, like in dexaflow.yaml: a misspelled key would otherwise be
// a setting that silently does nothing.
func parseWorkspaceFile(path string) (*workspaceSpec, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the workspace file the user is working in.
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	spec := &workspaceSpec{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if derr := dec.Decode(spec); derr != nil && !errors.Is(derr, io.EOF) {
		return nil, fmt.Errorf("parsing %s: %w", path, derr)
	}
	if verr := spec.validate(); verr != nil {
		return nil, fmt.Errorf("%s: %w", path, verr)
	}
	return spec, nil
}

// validate checks the target names, their servers and the default target.
func (s *workspaceSpec) validate() error {
	if s.SchemaVersion != "" && s.SchemaVersion != "1.0" {
		return fmt.Errorf("unsupported schema_version %q (this CLI reads 1.0)", s.SchemaVersion)
	}
	for _, name := range s.targetNames() {
		if err := validateTargetName(name); err != nil {
			return err
		}
		t := s.Targets[name]
		if t == nil || t.Server == "" {
			return fmt.Errorf("target %q has no server", name)
		}
		if err := validateServerURL(t.Server); err != nil {
			return fmt.Errorf("target %q: %w", name, err)
		}
	}
	if s.DefaultTarget != "" && s.Targets[s.DefaultTarget] == nil {
		return fmt.Errorf("default_target %q is not one of the targets (%s)", s.DefaultTarget, strings.Join(s.targetNames(), ", "))
	}
	return nil
}

// targetNames returns the target names in sorted order.
func (s *workspaceSpec) targetNames() []string {
	names := make([]string, 0, len(s.Targets))
	for name := range s.Targets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// workspaceEditor changes a workspace file through its YAML node tree, so the
// comments and the order a person gave the file survive the edit.
type workspaceEditor struct {
	root *yaml.Node // the document's top-level mapping
}

// editWorkspaceFile applies edit to the workspace file at path and writes it
// back, refusing the result if it would no longer be a valid workspace file.
func editWorkspaceFile(path string, edit func(*workspaceEditor) error) error {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the workspace file the user is working in.
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	var doc yaml.Node
	if uerr := yaml.Unmarshal(data, &doc); uerr != nil {
		return fmt.Errorf("parsing %s: %w", path, uerr)
	}
	if len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: the top level is not a mapping", path)
	}
	if eerr := edit(&workspaceEditor{root: root}); eerr != nil {
		return eerr
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	var check workspaceSpec
	if uerr := yaml.Unmarshal(buf.Bytes(), &check); uerr != nil {
		return fmt.Errorf("re-reading the edited %s: %w", path, uerr)
	}
	if verr := check.validate(); verr != nil {
		return fmt.Errorf("%s: %w", path, verr)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil { //nolint:gosec // G306: a shared, committed file with no secrets.
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// mappingIndex returns the index of key's key node in mapping m, or -1.
func mappingIndex(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// setMapping sets key to value in mapping m, keeping the key's position (and
// its comment) when it is already there.
func setMapping(m *yaml.Node, key string, value *yaml.Node) {
	if i := mappingIndex(m, key); i >= 0 {
		m.Content[i+1] = value
		return
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// deleteMapping removes key from mapping m and reports whether it was there.
func deleteMapping(m *yaml.Node, key string) bool {
	i := mappingIndex(m, key)
	if i < 0 {
		return false
	}
	m.Content = append(m.Content[:i], m.Content[i+2:]...)
	return true
}

// targets returns the targets mapping, creating it when create is set.
func (e *workspaceEditor) targets(create bool) *yaml.Node {
	if i := mappingIndex(e.root, "targets"); i >= 0 {
		n := e.root.Content[i+1]
		if n.Kind == yaml.MappingNode {
			return n
		}
		if !create {
			return nil
		}
		// `targets:` with no value, or `targets: ~`.
		m := &yaml.Node{Kind: yaml.MappingNode}
		e.root.Content[i+1] = m
		return m
	}
	if !create {
		return nil
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	setMapping(e.root, "targets", m)
	return m
}

// setTarget adds or replaces the target called name.
func (e *workspaceEditor) setTarget(name string, t workspaceTarget) error {
	var value yaml.Node
	if err := value.Encode(t); err != nil {
		return fmt.Errorf("encoding target %q: %w", name, err)
	}
	m := e.targets(true)
	// An empty `targets: {}` is a flow mapping; entries render one per line
	// only in block style.
	m.Style = 0
	setMapping(m, name, &value)
	return nil
}

// removeTarget removes the target called name and reports whether it existed.
func (e *workspaceEditor) removeTarget(name string) bool {
	m := e.targets(false)
	if m == nil {
		return false
	}
	return deleteMapping(m, name)
}

// setDefaultTarget sets default_target, or removes it when name is "".
func (e *workspaceEditor) setDefaultTarget(name string) {
	if name == "" {
		deleteMapping(e.root, "default_target")
		return
	}
	setMapping(e.root, "default_target", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: name})
}

// gitRootOf returns the top of the git work tree holding dir, or "" when dir
// is not in one or git is not installed.
func gitRootOf(ctx context.Context, dir string) string {
	c := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel") //nolint:gosec // G204: fixed git binary and arguments.
	c.Dir = dir
	c.Env = gitEnv(os.Environ())
	out, err := c.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
