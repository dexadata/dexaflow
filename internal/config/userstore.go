package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// UserStore is the per-user CLI state kept in the config file
// (~/.dexaflow/config.yaml): the single-server session written before named
// targets existed, the user's own targets, which target is selected, and one
// saved token per control plane.
//
// Tokens are keyed by the server URL they were issued for, never by a target
// name: a target is a label that a shared workspace file can point anywhere, so
// a token is only ever sent to the server that issued it.
type UserStore struct {
	// ServerURL and Token are the session `dexaflow auth login` wrote before
	// per-server sessions existed. They are read, and kept, as they are.
	ServerURL string `yaml:"server_url,omitempty"`
	Token     string `yaml:"token,omitempty"`
	// Registry is the user's default image registry prefix, the lowest-ranked
	// source a deploy reads one from.
	Registry string `yaml:"registry,omitempty"`
	// CurrentTarget is the target used outside a workspace (and inside one
	// that selects none).
	CurrentTarget string `yaml:"current_target,omitempty"`
	// Targets are the targets the user defined outside any workspace.
	Targets []UserTarget `yaml:"targets,omitempty"`
	// Sessions holds one saved token per control plane.
	Sessions []Session `yaml:"sessions,omitempty"`
	// WorkspaceTargets records `dexaflow target use` inside a workspace: the
	// user's own choice, which leaves the shared workspace file untouched.
	WorkspaceTargets []WorkspaceTarget `yaml:"workspace_targets,omitempty"`
	// TrustedWorkspaces lists workspace roots whose workspace file is used
	// even though another user owns it ("*" trusts every one). The CLI only
	// reads it; the user writes it.
	TrustedWorkspaces []string `yaml:"trusted_workspaces,omitempty"`
}

// UserTarget is a named control plane defined in the user's config file.
type UserTarget struct {
	Name      string   `yaml:"name"`
	Server    string   `yaml:"server"`
	Registry  string   `yaml:"registry,omitempty"`
	Platforms []string `yaml:"platforms,omitempty"`
	BaseImage string   `yaml:"base_image,omitempty"`
	// SourceModeImage is the runtime image deploys to this target register
	// dag.py on, with no build (source mode, #1772).
	SourceModeImage string `yaml:"source_mode_image,omitempty"`
}

// Session is a token saved for one control plane.
type Session struct {
	Server string `yaml:"server"`
	Token  string `yaml:"token"`
}

// WorkspaceTarget is the target the user selected for one workspace root.
type WorkspaceTarget struct {
	Workspace string `yaml:"workspace"`
	Target    string `yaml:"target"`
}

// NormalizeServer is the form a server URL is compared and stored in: trimmed,
// without trailing slashes. Anything cleverer (default ports, host casing)
// could make two different control planes share a token.
func NormalizeServer(url string) string {
	return strings.TrimRight(strings.TrimSpace(url), "/")
}

// ReadUserStore reads the user store from the config file at path. A missing
// file is an empty store.
func ReadUserStore(path string) (*UserStore, error) {
	s := &UserStore{}
	if path == "" {
		return s, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the CLI's own config file.
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	if uerr := yaml.Unmarshal(data, s); uerr != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, uerr)
	}
	return s, nil
}

// UpdateUserStore reads the store at path, lets update change it, and writes
// the managed keys back (the per-server sessions, the targets, the selections
// and the single-server server_url/token pair). Every other key, the order of
// the keys and their comments are kept. The file is created when absent and
// replaced atomically at 0600, whatever its mode was, because it holds tokens.
func UpdateUserStore(path string, update func(*UserStore) error) error {
	if path == "" {
		return fmt.Errorf("updating config: no config file path")
	}
	doc, err := readDocument(path)
	if err != nil {
		return err
	}
	s, err := ReadUserStore(path)
	if err != nil {
		return err
	}
	if uerr := update(s); uerr != nil {
		return uerr
	}
	root := doc.Content[0]
	managed := []struct {
		key   string
		value any
		empty bool
	}{
		{"server_url", s.ServerURL, s.ServerURL == ""},
		{"token", s.Token, s.Token == ""},
		{"current_target", s.CurrentTarget, s.CurrentTarget == ""},
		{"targets", s.Targets, len(s.Targets) == 0},
		{"sessions", s.Sessions, len(s.Sessions) == 0},
		{"workspace_targets", s.WorkspaceTargets, len(s.WorkspaceTargets) == 0},
	}
	for _, m := range managed {
		if m.empty {
			deleteKey(root, m.key)
			continue
		}
		var n yaml.Node
		if eerr := n.Encode(m.value); eerr != nil {
			return fmt.Errorf("encoding %s: %w", m.key, eerr)
		}
		setKey(root, m.key, &n)
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	return writeSecretFile(path, out)
}

// readDocument parses the config file into a YAML document whose top level
// is a mapping, or an empty one when the file does not exist or is empty.
func readDocument(path string) (*yaml.Node, error) {
	doc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the CLI's own config file.
	if os.IsNotExist(err) {
		return doc, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	var parsed yaml.Node
	if uerr := yaml.Unmarshal(data, &parsed); uerr != nil {
		return nil, fmt.Errorf("parsing existing config %q: %w", path, uerr)
	}
	if len(parsed.Content) == 0 {
		return doc, nil
	}
	if parsed.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parsing existing config %q: the top level is not a mapping", path)
	}
	return &parsed, nil
}

// setKey sets key to value in mapping m, in place when it is already there,
// keeping the comment written at the end of the old value's line.
func setKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			if value.LineComment == "" {
				value.LineComment = m.Content[i+1].LineComment
			}
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// deleteKey removes key from mapping m. The comment above the key is moved
// to the key that follows it (or below the one before it), so removing a
// value never deletes what a person wrote about the file.
func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		if head := m.Content[i].HeadComment; head != "" {
			switch {
			case i+2 < len(m.Content):
				next := m.Content[i+2]
				next.HeadComment = joinComments(head, next.HeadComment)
			case i >= 2:
				prev := m.Content[i-1]
				prev.FootComment = joinComments(prev.FootComment, head)
			default:
				m.HeadComment = joinComments(m.HeadComment, head)
			}
		}
		m.Content = append(m.Content[:i], m.Content[i+2:]...)
		return
	}
}

// joinComments joins two comment blocks, either of which may be empty.
func joinComments(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "\n" + b
}

// writeSecretFile replaces path with data atomically: a 0600 temporary file in
// the same directory, renamed over it, so a reader never sees half a file and
// the result is 0600 even when the old file was not.
//
// A config file that is a symlink (kept in a dotfiles repository, say) is
// written through: the link stays and its target is replaced.
func writeSecretFile(path string, data []byte) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("writing config %q: %w", path, err)
	}
	name := tmp.Name()
	defer os.Remove(name) //nolint:errcheck // gone after the rename; cleanup only on failure.
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close() //nolint:errcheck // the write error is the one reported.
		return fmt.Errorf("writing config %q: %w", path, werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return fmt.Errorf("writing config %q: %w", path, cerr)
	}
	if rerr := os.Rename(name, path); rerr != nil {
		return fmt.Errorf("writing config %q: %w", path, rerr)
	}
	return nil
}

// Session returns the token saved for server, if any.
func (s *UserStore) Session(server string) (string, bool) {
	key := NormalizeServer(server)
	for _, sess := range s.Sessions {
		if NormalizeServer(sess.Server) == key && sess.Token != "" {
			return sess.Token, true
		}
	}
	return "", false
}

// SetSession saves token for server, replacing the one saved before.
func (s *UserStore) SetSession(server, token string) {
	key := NormalizeServer(server)
	for i := range s.Sessions {
		if NormalizeServer(s.Sessions[i].Server) == key {
			s.Sessions[i] = Session{Server: key, Token: token}
			return
		}
	}
	s.Sessions = append(s.Sessions, Session{Server: key, Token: token})
}

// RemoveSession forgets the token saved for server and reports whether there
// was one.
func (s *UserStore) RemoveSession(server string) bool {
	key := NormalizeServer(server)
	for i := range s.Sessions {
		if NormalizeServer(s.Sessions[i].Server) == key {
			s.Sessions = append(s.Sessions[:i], s.Sessions[i+1:]...)
			return true
		}
	}
	return false
}

// Target returns the user target called name.
func (s *UserStore) Target(name string) (UserTarget, bool) {
	for _, t := range s.Targets {
		if t.Name == name {
			return t, true
		}
	}
	return UserTarget{}, false
}

// SetTarget adds t, or replaces the user target of the same name.
func (s *UserStore) SetTarget(t UserTarget) {
	for i := range s.Targets {
		if s.Targets[i].Name == t.Name {
			s.Targets[i] = t
			return
		}
	}
	s.Targets = append(s.Targets, t)
}

// RemoveTarget removes the user target called name and reports whether it
// existed.
func (s *UserStore) RemoveTarget(name string) bool {
	for i := range s.Targets {
		if s.Targets[i].Name == name {
			s.Targets = append(s.Targets[:i], s.Targets[i+1:]...)
			return true
		}
	}
	return false
}

// WorkspaceTarget returns the target the user selected for the workspace at
// root, or "".
func (s *UserStore) WorkspaceTarget(root string) string {
	root = filepath.Clean(root)
	for _, w := range s.WorkspaceTargets {
		if filepath.Clean(w.Workspace) == root {
			return w.Target
		}
	}
	return ""
}

// SetWorkspaceTarget records name as the user's target for the workspace at
// root; an empty name clears the selection.
func (s *UserStore) SetWorkspaceTarget(root, name string) {
	root = filepath.Clean(root)
	for i := range s.WorkspaceTargets {
		if filepath.Clean(s.WorkspaceTargets[i].Workspace) == root {
			if name == "" {
				s.WorkspaceTargets = append(s.WorkspaceTargets[:i], s.WorkspaceTargets[i+1:]...)
				return
			}
			s.WorkspaceTargets[i].Target = name
			return
		}
	}
	if name != "" {
		s.WorkspaceTargets = append(s.WorkspaceTargets, WorkspaceTarget{Workspace: root, Target: name})
	}
}
