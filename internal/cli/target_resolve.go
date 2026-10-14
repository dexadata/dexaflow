package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/envcompat"
)

// Where a target is defined.
const (
	scopeWorkspace = "workspace"
	scopeUser      = "user"
)

// activeTarget is the named control plane a command talks to, and why.
type activeTarget struct {
	// Name is the target's name.
	Name string
	// Server is its control plane base URL.
	Server string
	// Defaults are its registry, platforms and base image, if any.
	Defaults deployDefaults
	// Scope is scopeWorkspace or scopeUser.
	Scope string
	// Origin says what selected it, in words a user can act on.
	Origin string
	// explicit marks a selection by --target or DEXAFLOW_TARGET, which beats
	// a server URL from the environment.
	explicit bool
}

// targetContext is everything target resolution reads: the workspace around
// the command's work dir (findWorkspaceFor) and the user's config file.
type targetContext struct {
	ws        *workspaceLocation
	store     *config.UserStore
	storePath string
}

// loadTargetContext reads the workspace around the command's work dir (the
// current directory unless withWorkDir set another) and the config file the
// command uses.
func loadTargetContext(cmd *cobra.Command) (*targetContext, error) {
	ws, err := findWorkspaceFor(cmd)
	if err != nil {
		return nil, err
	}
	path := configFilePath(cmd)
	store, err := config.ReadUserStore(path)
	if err != nil {
		return nil, err
	}
	return &targetContext{ws: ws, store: store, storePath: path}, nil
}

// lookup finds a target by name: the workspace's first, then the user's.
func (tc *targetContext) lookup(name string) (*activeTarget, bool) {
	if tc.ws != nil {
		if t := tc.ws.Spec.Targets[name]; t != nil {
			return &activeTarget{Name: name, Server: t.Server, Defaults: t.deployDefaults, Scope: scopeWorkspace}, true
		}
	}
	if t, ok := tc.store.Target(name); ok {
		return &activeTarget{Name: name, Server: t.Server, Scope: scopeUser,
			Defaults: deployDefaults{Registry: t.Registry, Platforms: t.Platforms, BaseImage: t.BaseImage}}, true
	}
	return nil, false
}

// knownTargets lists every target name the context can resolve, sorted, with
// workspace targets first.
func (tc *targetContext) knownTargets() []string {
	var names []string
	seen := map[string]bool{}
	if tc.ws != nil {
		for _, n := range tc.ws.Spec.targetNames() {
			names = append(names, n)
			seen[n] = true
		}
	}
	for _, t := range tc.store.Targets {
		if !seen[t.Name] {
			names = append(names, t.Name)
		}
	}
	return names
}

// targetFlag returns the global --target flag's value, "" when the command
// tree has no such flag (a subcommand built on its own in a test).
func targetFlag(cmd *cobra.Command) string {
	if f := cmd.Flag("target"); f != nil {
		return strings.TrimSpace(f.Value.String())
	}
	return ""
}

// selection decides which target name is selected and what selected it. The
// order is: --target, DEXAFLOW_TARGET (or LEOFLOW_TARGET), the user's choice
// for this workspace (`dexaflow target use`), the workspace's default_target,
// then the user's current_target.
func (tc *targetContext) selection(cmd *cobra.Command) (name, origin string, explicit bool) {
	if n := targetFlag(cmd); n != "" {
		return n, "--target", true
	}
	if n, ok := envcompat.Lookup("TARGET"); ok {
		return n, "DEXAFLOW_TARGET", true
	}
	if tc.ws != nil {
		if n := tc.store.WorkspaceTarget(tc.ws.Root); n != "" {
			return n, "selected for this workspace with `dexaflow target use`", false
		}
		if n := tc.ws.Spec.DefaultTarget; n != "" {
			return n, "default_target in " + tc.ws.File, false
		}
	}
	if n := tc.store.CurrentTarget; n != "" {
		return n, "current_target in " + tc.storePath, false
	}
	return "", "", false
}

// selectTarget returns the selected target, or nil when none is selected. A
// selected name that is not defined anywhere is an error naming the ones that
// are, never a silent fall back to another server.
func (tc *targetContext) selectTarget(cmd *cobra.Command) (*activeTarget, error) {
	name, origin, explicit := tc.selection(cmd)
	if name == "" {
		return nil, nil //nolint:nilnil // no target selected is a normal outcome, not an error.
	}
	t, ok := tc.lookup(name)
	if !ok {
		known := tc.knownTargets()
		if len(known) == 0 {
			return nil, fmt.Errorf("unknown target %q (from %s): no targets are defined; add one with `dexaflow target add %s <url>`", name, origin, name)
		}
		return nil, fmt.Errorf("unknown target %q (from %s); known targets: %s", name, origin, strings.Join(known, ", "))
	}
	t.Origin, t.explicit = origin, explicit
	return t, nil
}

// serverChoice is the outcome of resolveServer.
type serverChoice struct {
	// URL is the control plane to call.
	URL string
	// Target is the target URL came from, nil when it came from a flag, the
	// environment or the config file's server_url.
	Target *activeTarget
	// Source says what chose URL, in words a user can act on.
	Source string
	// Implicit is true when nothing chose URL: no flag, target, variable or
	// server_url, so it is the built-in http://localhost:8080.
	Implicit bool
	// Lite is true when the config file was written for Lite (`dexaflow
	// setup`), where the local server is the expected one.
	Lite bool
	tc   *targetContext
}

// resolveServer decides which control plane a command calls:
//
//  1. --server (each server command's own flag), then --server-url;
//  2. --target, then DEXAFLOW_TARGET;
//  3. DEXAFLOW_SERVER_URL;
//  4. the workspace's or the user's selected target;
//  5. server_url in the config file, then http://localhost:8080.
func resolveServer(cmd *cobra.Command, serverFlag string) (*serverChoice, error) {
	tc, err := loadTargetContext(cmd)
	if err != nil {
		return nil, err
	}
	if serverFlag != "" {
		return &serverChoice{URL: serverFlag, Source: "--server", tc: tc}, nil
	}
	if f := cmd.Flag("server-url"); f != nil && f.Changed {
		return &serverChoice{URL: f.Value.String(), Source: "--server-url", tc: tc}, nil
	}
	t, err := tc.selectTarget(cmd)
	if err != nil {
		return nil, err
	}
	_, envServer := envcompat.Lookup("SERVER_URL")
	if t != nil && (t.explicit || !envServer) {
		return &serverChoice{URL: t.Server, Target: t, Source: fmt.Sprintf("target %s, from %s", t.Name, t.Origin), tc: tc}, nil
	}
	cfg, err := config.Load(configFilePath(cmd), cmd.Flags())
	if err != nil {
		return nil, err
	}
	source := "the default"
	implicit := false
	switch {
	case envServer:
		source = "DEXAFLOW_SERVER_URL"
	case tc.store.ServerURL != "":
		source = "server_url in " + tc.storePath
	default:
		implicit = true
	}
	return &serverChoice{URL: cfg.ServerURL, Source: source, tc: tc,
		Implicit: implicit, Lite: cfg.LiteExecutor != ""}, nil
}

// tokenSource says where resolveServerToken found the token.
type tokenSource int

const (
	tokenNone     tokenSource = iota
	tokenExternal             // --token, DEXAFLOW_TOKEN or LEOFLOW_TOKEN
	tokenSession              // saved for this server by `auth login`
	tokenLegacy               // the single-server session from before targets
)

// pickTokenFor applies the token precedence for server: --token, then
// DEXAFLOW_TOKEN (LEOFLOW_TOKEN), then the session saved for that server, then
// the single-server session. The single-server token is only sent to the
// server it was saved with (or anywhere when it was saved with none): whatever
// chose the server, a flag, a variable or a target name in a shared workspace
// file, a token must never follow it to another control plane. With ownOnly
// a single-server token saved with no server_url is withheld too:
// `status --all-targets` calls every target in turn, and that one token must
// not go to all of them.
func pickTokenFor(choice *serverChoice, tokenFlag string, ownOnly bool) (string, tokenSource) {
	if tokenFlag != "" {
		return tokenFlag, tokenExternal
	}
	if v, ok := envcompat.Lookup("TOKEN"); ok {
		return v, tokenExternal
	}
	store := choice.tc.store
	if tok, ok := store.Session(choice.URL); ok {
		return tok, tokenSession
	}
	if store.Token == "" {
		return "", tokenNone
	}
	if (store.ServerURL == "" && ownOnly) || (store.ServerURL != "" && !sameServer(store.ServerURL, choice.URL)) {
		return "", tokenNone
	}
	return store.Token, tokenLegacy
}

// saveSession saves token as the session for server in the config file the
// command writes to.
func saveSession(cmd *cobra.Command, server, token string) error {
	path, err := sessionConfigPath(cmd)
	if err != nil {
		return err
	}
	return config.UpdateUserStore(path, func(s *config.UserStore) error {
		s.SetSession(server, token)
		return nil
	})
}
