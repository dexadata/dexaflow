package cli

import (
	"os"
	"regexp"
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
// Empty fields for a missing or unreadable file: the caller is a best-effort
// sync and an absent config is not a failure.
func configFileSecrets(path string) liteFileSecrets {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-owned path under the user's home
	if err != nil {
		return liteFileSecrets{}
	}
	body := string(raw)
	return liteFileSecrets{
		jwtSecret:         quotedScalar(body, "jwt_secret"),
		secretKey:         quotedScalar(body, "secret_key"),
		secretKeyPrevious: quotedScalar(body, "secret_key_previous"),
	}
}

// quotedScalar returns the value of a top-level `name: "value"` line. Anchored
// per line and to the start of the line so `secret_key_previous` is never read
// as `secret_key`, and a mention inside a comment is never read at all.
func quotedScalar(body, name string) string {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `:\s+"([^"]*)"\s*$`)
	m := re.FindStringSubmatch(body)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}
