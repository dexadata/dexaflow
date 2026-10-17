package cli

import (
	"fmt"
	"io"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// registryCloud is the cloud session the registry client reads; nil reads
// the cloud CLIs on PATH (build.CloudCLI). It is a variable only so the
// tests of this package stand in a fake session for a local registry;
// nothing else assigns it.
var registryCloud build.CloudKeychain

// registryFor is the registry client of a project: its credentials are
// read as cfg's registry.auth_method says (ADR 0080). Validate has refused
// any value ParseAuthMethod does not know; a config that was not validated
// reads as auto.
func registryFor(cfg *domain.LeoflowConfig) build.Registry {
	return build.Registry{Auth: authMethodOf(cfg), Cloud: registryCloud}
}

// authMethodOf is cfg's registry.auth_method, auto when unset or unknown.
func authMethodOf(cfg *domain.LeoflowConfig) build.AuthMethod {
	if cfg == nil || cfg.Registry == nil {
		return build.AuthAuto
	}
	m, _, err := build.ParseAuthMethod(cfg.Registry.AuthMethod)
	if err != nil {
		return build.AuthAuto
	}
	return m
}

// warnAuthMethod prints the note of a deprecated registry.auth_method
// (env, iam), which still loads, to w.
func warnAuthMethod(w io.Writer, cfg *domain.LeoflowConfig) {
	if cfg == nil || cfg.Registry == nil {
		return
	}
	if _, note, err := build.ParseAuthMethod(cfg.Registry.AuthMethod); err == nil && note != "" {
		_, _ = fmt.Fprintf(w, "warning: %s\n", note) //nolint:errcheck // advisory
	}
}
