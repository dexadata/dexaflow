package cli

import (
	"os"

	"gopkg.in/yaml.v3"
)

// liteFileSecrets are the secrets as ~/.leoflow/config.yaml holds them, with no
// environment overlay.
type liteFileSecrets struct {
	jwtSecret         string
	secretKey         string
	secretKeyPrevious string
}

// configFileSecrets reads the secrets straight from the config file.
//
// config.Load exists for reading configuration, and it deliberately overlays
// LEOFLOW_* environment variables on top of the file. That is right for a
// process deciding how to behave, and wrong for a command that REWRITES the
// file: an operator with LEOFLOW_SECRET_KEY exported in their shell would have
// the shell value written over the per-install key, and `leoflow lite
// reset-password` would silently destroy the only copy of the key that decrypts
// every stored connection. This repository's own end-to-end scripts export that
// variable, so it is not a hypothetical shell.
//
// It parses YAML rather than matching lines. A line-matching version of this
// read `secret_key: abc` (unquoted), `secret_key: 'abc'` (single-quoted) and
// `secret_key: "abc" # note` as EMPTY, all of which are valid YAML that the
// loader accepts. A rewrite then persisted an empty key, and the install lost
// the only copy of it.
//
// Empty fields for a missing or unparseable file: the caller is a best-effort
// sync, and refusing there would block a password reset over a comment.
func configFileSecrets(path string) liteFileSecrets {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-owned path under the user's home
	if err != nil {
		return liteFileSecrets{}
	}
	var doc struct {
		JWTSecret         string `yaml:"jwt_secret"`
		SecretKey         string `yaml:"secret_key"`
		SecretKeyPrevious string `yaml:"secret_key_previous"`
	}
	if uerr := yaml.Unmarshal(raw, &doc); uerr != nil {
		return liteFileSecrets{}
	}
	return liteFileSecrets{
		jwtSecret:         doc.JWTSecret,
		secretKey:         doc.SecretKey,
		secretKeyPrevious: doc.SecretKeyPrevious,
	}
}
