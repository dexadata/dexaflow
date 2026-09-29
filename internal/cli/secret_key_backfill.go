package cli

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// secretKeyLineRe matches an existing `secret_key:` entry at the top level of
// the Lite config. Anchored per line so a key mentioned inside a comment or a
// nested block is never mistaken for the setting.
var secretKeyLineRe = regexp.MustCompile(`(?m)^secret_key:\s`)

// backfillSecretKey gives an install created before per-install keys a real one
// (#486), and returns the key now in the file.
//
// It APPENDS a line rather than rewriting the file. A rewrite has to reconstruct
// every other field from a loaded config, and that load overlays LEOFLOW_*
// environment variables on top of the file: an operator with LEOFLOW_SECRET_KEY
// exported in their shell would have the shell value written over the real one,
// destroying the only copy of the key that decrypts their connections. Appending
// one line cannot do that to any field it does not name.
//
// It is idempotent: a second call returns the key already there. Generating a
// new one on every boot would orphan everything the previous boot wrote.
func backfillSecretKey(configPath string) (string, error) {
	raw, err := os.ReadFile(configPath) //nolint:gosec // operator-owned path under the user's home
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", configPath, err)
	}
	if loc := secretKeyLineRe.FindIndex(raw); loc != nil {
		existing := existingSecretKey(string(raw))
		if existing == "" {
			return "", fmt.Errorf("%s has a secret_key line this tool cannot read; edit it by hand", configPath)
		}
		return existing, nil
	}

	key, gerr := generateSecretKey()
	if gerr != nil {
		return "", gerr
	}
	body := string(raw)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += fmt.Sprintf("secret_key: %q\n", key)
	// Record that a predecessor is still needed. Without this the next boot
	// cannot tell a freshly generated install (nothing to migrate) from one that
	// still has rows under the published constant, and the only safe default
	// would be to keep that constant in every install's read set forever,
	// including installs that never wrote a byte with it.
	//
	// The re-encryption pass removes this line when no row needs it any more,
	// which is what finally lets the constant be deleted from the source.
	body += fmt.Sprintf("secret_key_previous: %q\n", devSecretKey)
	// 0600, not the file's current mode: this file now holds the only copy of
	// the key that decrypts every stored credential.
	// #nosec G304 G703 -- configPath is this CLI's own --config flag (or the
	// default under the invoking user's home), the same path it just read above.
	// The command runs as the user whose config it edits; there is no boundary
	// to cross.
	if werr := os.WriteFile(configPath, []byte(body), 0o600); werr != nil {
		return "", fmt.Errorf("writing %s: %w", configPath, werr)
	}
	return key, nil
}

// existingSecretKey pulls the quoted value out of a `secret_key: "..."` line.
// Returns empty when the line is present but not in the shape this tool writes,
// so the caller refuses rather than guessing at an operator's hand edit.
func existingSecretKey(body string) string {
	m := regexp.MustCompile(`(?m)^secret_key:\s+"([^"]*)"\s*$`).FindStringSubmatch(body)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}
