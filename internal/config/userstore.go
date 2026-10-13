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
}

// UserTarget is a named control plane defined in the user's config file.
type UserTarget struct {
	Name      string   `yaml:"name"`
	Server    string   `yaml:"server"`
	Registry  string   `yaml:"registry,omitempty"`
	Platforms []string `yaml:"platforms,omitempty"`
	BaseImage string   `yaml:"base_image,omitempty"`
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

// managedKeys are the config keys UpdateUserStore owns. Every other key in the
// file (Lite settings, the legacy session) is written back untouched.
var managedKeys = []string{"current_target", "targets", "sessions", "workspace_targets"}

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
// the managed keys back, keeping every other key in the file. The file is
// created when absent and kept at 0600, because it holds tokens.
func UpdateUserStore(path string, update func(*UserStore) error) error {
	if path == "" {
		return fmt.Errorf("updating config: no config file path")
	}
	values, err := readValues(path)
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
	managed := map[string]any{
		"current_target":    s.CurrentTarget,
		"targets":           s.Targets,
		"sessions":          s.Sessions,
		"workspace_targets": s.WorkspaceTargets,
	}
	for _, k := range managedKeys {
		if isEmptyValue(managed[k]) {
			delete(values, k)
			continue
		}
		values[k] = managed[k]
	}
	return writeValues(path, values)
}

// readValues decodes the config file into a generic map, or an empty map when
// the file does not exist.
func readValues(path string) (map[string]any, error) {
	values := map[string]any{}
	data, err := os.ReadFile(path) //nolint:gosec // G304: the CLI's own config file.
	if os.IsNotExist(err) {
		return values, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}
	if uerr := yaml.Unmarshal(data, &values); uerr != nil {
		return nil, fmt.Errorf("parsing existing config %q: %w", path, uerr)
	}
	if values == nil {
		values = map[string]any{}
	}
	return values, nil
}

// writeValues encodes values into the config file at path, at 0600.
func writeValues(path string, values map[string]any) error {
	out, err := yaml.Marshal(values)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("writing config %q: %w", path, err)
	}
	return nil
}

func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case []UserTarget:
		return len(x) == 0
	case []Session:
		return len(x) == 0
	case []WorkspaceTarget:
		return len(x) == 0
	}
	return v == nil
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
	for _, w := range s.WorkspaceTargets {
		if w.Workspace == root {
			return w.Target
		}
	}
	return ""
}

// SetWorkspaceTarget records name as the user's target for the workspace at
// root; an empty name clears the selection.
func (s *UserStore) SetWorkspaceTarget(root, name string) {
	for i := range s.WorkspaceTargets {
		if s.WorkspaceTargets[i].Workspace == root {
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
