package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// PersistSession writes the control-plane server URL and auth token into the
// config file at path, preserving any other keys already there (e.g. the Lite
// settings written by `dexaflow setup`). It creates the file and its parent
// directory when absent, and keeps the file at 0600 because the token is a
// secret. An empty path is an error: the caller must resolve the target first.
func PersistSession(path, serverURL, token string) error {
	if path == "" {
		return fmt.Errorf("persisting session: no config file path")
	}

	values := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if uerr := yaml.Unmarshal(data, &values); uerr != nil {
			return fmt.Errorf("parsing existing config %q: %w", path, uerr)
		}
		if values == nil {
			values = map[string]any{}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading config %q: %w", path, err)
	}

	values["server_url"] = serverURL
	values["token"] = token

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
