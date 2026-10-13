package config

import (
	"fmt"
)

// PersistSession writes the control-plane server URL and auth token into the
// config file at path, preserving any other keys already there (e.g. the Lite
// settings written by `dexaflow setup`). It creates the file and its parent
// directory when absent, and replaces the file atomically at 0600 because the token is a
// secret. An empty path is an error: the caller must resolve the target first.
func PersistSession(path, serverURL, token string) error {
	if path == "" {
		return fmt.Errorf("persisting session: no config file path")
	}
	return UpdateUserStore(path, func(s *UserStore) error {
		s.ServerURL, s.Token = serverURL, token
		return nil
	})
}
