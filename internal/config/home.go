package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// HomeDirName is the per-user state directory under the home directory.
const HomeDirName = ".dexaflow"

// LegacyHomeDirName is the state directory's name before the rename.
const LegacyHomeDirName = ".leoflow"

// HomeDir returns the per-user state directory, ~/.dexaflow. See HomeDirIn.
func HomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return HomeDirIn(home)
}

// HomeDirIn returns the state directory under userHome. It is ~/.dexaflow,
// except for an install from before the rename: then ~/.dexaflow is created as
// a link to the existing ~/.leoflow, so its data stays where it is (a running
// Lite holds files and mounts under it) and both paths reach it. If the link
// cannot be created, ~/.leoflow itself is returned, so state is never split
// between two directories. The directory is not created here.
func HomeDirIn(userHome string) (string, error) {
	current := filepath.Join(userHome, HomeDirName)
	if _, err := os.Lstat(current); err == nil {
		return current, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("checking %s: %w", current, err)
	}
	legacy := filepath.Join(userHome, LegacyHomeDirName)
	if !isDir(legacy) {
		return current, nil
	}
	if !linkTo(LegacyHomeDirName, current) {
		// Fall back to the legacy home rather than fail: it holds the state.
		return legacy, nil
	}
	return current, nil
}

// isDir reports whether path is an existing directory.
func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// linkTo creates link as a symlink to target and reports whether it did.
func linkTo(target, link string) bool {
	return os.Symlink(target, link) == nil
}
