package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
)

// newTargetCommand builds `dexaflow target`: named control planes, so moving
// between Lite, a dev cluster and production is a name rather than a URL and a
// token juggled by hand (#1102, #1704). Inside a workspace the targets live in
// the shared dexaflow.workspace.yaml; outside one, or with --global, in the
// user's config file. Credentials always stay in the user's config file, saved
// per server by `dexaflow auth login --target <name>`.
func newTargetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "target",
		Short: "Manage named control planes (targets) to deploy to and query.",
		Long: "A target names a control plane. Inside a workspace, targets are kept in\n" +
			"dexaflow.workspace.yaml and shared with everyone who clones it; outside a\n" +
			"workspace, or with --global, they are kept in your config file.\n\n" +
			"Every server command takes --target <name>. The server a command calls is:\n" +
			"--server or --server-url; then --target, then DEXAFLOW_TARGET; then\n" +
			"DEXAFLOW_SERVER_URL; then the target chosen with `dexaflow target use` for this\n" +
			"workspace, the workspace's default_target, or current_target from the config\n" +
			"file; then server_url from the config file.\n\n" +
			"Tokens are never stored in the workspace file: `dexaflow auth login --target\n" +
			"<name>` saves one per server in your config file.",
	}
	cmd.AddCommand(newTargetAddCommand(), newTargetUseCommand(), newTargetListCommand(), newTargetRemoveCommand())
	return cmd
}

// targetAddOptions holds the flags of `target add`.
type targetAddOptions struct {
	defaults deployDefaults
	global   bool
	force    bool
	// autoSelect lets a first target become the default; runTargetAdd sets it.
	autoSelect bool
}

func newTargetAddCommand() *cobra.Command {
	var o targetAddOptions
	cmd := &cobra.Command{
		Use:   "add <name> <server-url>",
		Short: "Add a target (to the workspace file, or your config with --global).",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTargetAdd(cmd, args[0], strings.TrimSpace(args[1]), o)
		},
	}
	cmd.Flags().StringVar(&o.defaults.Registry, "registry", "", "image registry prefix deploys to this target push to (e.g. ghcr.io/acme)")
	cmd.Flags().StringSliceVar(&o.defaults.Platforms, "platform", nil, "image platform for deploys to this target (repeatable, e.g. linux/amd64)")
	cmd.Flags().StringVar(&o.defaults.BaseImage, "base-image", "", "base image for DAGs that set none, when deploying to this target")
	cmd.Flags().StringVar(&o.defaults.SourceModeImage, "source-mode-image", "", "runtime image, pinned by digest, deploys to this target register dag.py on with no build (the server's execution.source_mode.image), or none to build even where the workspace defaults or the cluster set one")
	cmd.Flags().BoolVar(&o.global, "global", false, "keep the target in your config file even inside a workspace")
	cmd.Flags().BoolVar(&o.force, "force", false, "replace a target of the same name")
	return cmd
}

// validateTargetAdd refuses a target name, a server URL or a source mode
// image that `target add` cannot store.
func validateTargetAdd(name, server string, o targetAddOptions) error {
	if err := validateTargetName(name); err != nil {
		return err
	}
	if err := validateServerURL(server); err != nil {
		return err
	}
	if img := o.defaults.SourceModeImage; img != "" && img != sourceModeNone {
		if err := checkSourceModeImage(img); err != nil {
			return fmt.Errorf("--source-mode-image %q %v; give the server's execution.source_mode.image byte for byte, "+
				"or none to build even where the workspace defaults or the cluster set one", printable(img), err)
		}
	}
	return nil
}

func runTargetAdd(cmd *cobra.Command, name, server string, o targetAddOptions) error {
	if err := validateTargetAdd(name, server, o); err != nil {
		return err
	}
	tc, err := loadTargetContext(cmd)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	// A first target is selected automatically, unless the user already works
	// against the config file's server_url: adding a target must not move
	// their commands to another server behind their back.
	// Only the user's own current_target follows this rule: the workspace file
	// is shared, so its default_target never depends on one author's config.
	o.autoSelect = tc.store.ServerURL == ""
	var where string
	var isDefault bool
	if tc.ws != nil && !o.global {
		where, isDefault, err = addWorkspaceTarget(tc.ws, name, server, o)
	} else {
		where, isDefault, err = addUserTarget(cmd, name, server, o)
	}
	if err != nil {
		return err
	}
	if _, werr := fmt.Fprintf(out, "Added target %s (%s) to %s\n", name, server, where); werr != nil {
		return werr
	}
	inWorkspace := tc.ws != nil && !o.global
	if werr := printSelectionNote(out, name, isDefault, o.autoSelect || inWorkspace, tc.store.ServerURL); werr != nil {
		return werr
	}
	if werr := printOverrideNote(out, name, isDefault && inWorkspace, tc); werr != nil {
		return werr
	}
	if _, saved := tc.store.Session(server); saved {
		_, err = fmt.Fprintf(out, "A session for %s is already saved.\n", server)
		return err
	}
	_, err = fmt.Fprintf(out, "\nNext: sign in once with\n  dexaflow auth login --target %s\n", name)
	return err
}

// printSelectionNote says whether the new target is now the one in use, and
// how to select it when server_url kept it from being selected.
func printSelectionNote(out io.Writer, name string, isDefault, autoSelect bool, serverURL string) error {
	switch {
	case isDefault:
		_, err := fmt.Fprintf(out, "%s is now the default target.\n", name)
		return err
	case !autoSelect:
		_, err := fmt.Fprintf(out, "Commands keep using server_url (%s) from your config file; to use this target instead:\n  dexaflow target use %s\n", serverURL, name)
		return err
	}
	return nil
}

// printOverrideNote tells the author when a new workspace default replaces,
// inside this workspace, the server_url their config file points everything
// else at. A target they chose for the workspace with `target use` still wins,
// so then nothing changes for them.
func printOverrideNote(out io.Writer, name string, newDefault bool, tc *targetContext) error {
	if !newDefault || tc.store.ServerURL == "" || tc.store.WorkspaceTarget(tc.ws.Root) != "" {
		return nil
	}
	_, err := fmt.Fprintf(out, "Inside this workspace, commands now use target %s instead of server_url (%s) from your config file; outside it, server_url still applies.\n", name, tc.store.ServerURL)
	return err
}

// addWorkspaceTarget writes the target into the workspace file, making it the
// default when the workspace has none.
func addWorkspaceTarget(ws *workspaceLocation, name, server string, o targetAddOptions) (where string, isDefault bool, err error) {
	if old := ws.Spec.Targets[name]; old != nil && !o.force {
		return "", false, fmt.Errorf("target %q already exists in %s (server %s); pass --force to replace it", name, ws.File, old.Server)
	}
	isDefault = ws.Spec.DefaultTarget == ""
	err = editWorkspaceFile(ws.File, func(e *workspaceEditor) error {
		if serr := e.setTarget(name, workspaceTarget{Server: server, deployDefaults: o.defaults}); serr != nil {
			return serr
		}
		if isDefault {
			e.setDefaultTarget(name)
		}
		return nil
	})
	return ws.File, isDefault, err
}

// addUserTarget writes the target into the user's config file, making it the
// current target when none is.
func addUserTarget(cmd *cobra.Command, name, server string, o targetAddOptions) (where string, isDefault bool, err error) {
	path, err := sessionConfigPath(cmd)
	if err != nil {
		return "", false, err
	}
	err = config.UpdateUserStore(path, func(s *config.UserStore) error {
		if old, ok := s.Target(name); ok && !o.force {
			return fmt.Errorf("target %q already exists in %s (server %s); pass --force to replace it", name, path, old.Server)
		}
		s.SetTarget(config.UserTarget{Name: name, Server: server, Registry: o.defaults.Registry,
			Platforms: o.defaults.Platforms, BaseImage: o.defaults.BaseImage, SourceModeImage: o.defaults.SourceModeImage})
		if s.CurrentTarget == "" && o.autoSelect {
			s.CurrentTarget = name
			isDefault = true
		}
		return nil
	})
	return path, isDefault, err
}

func newTargetUseCommand() *cobra.Command {
	var global, workspaceDefault bool
	cmd := &cobra.Command{
		Use:   "use <name>",
		Short: "Select the target commands use when no --target is given.",
		Long: "Inside a workspace, the choice is yours alone: it is saved in your config file\n" +
			"for that workspace and the shared file is left untouched. --workspace-default\n" +
			"changes the workspace's default_target instead, for everyone who clones it.\n" +
			"Outside a workspace, or with --global, it sets the target used everywhere else.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTargetUse(cmd, args[0], global, workspaceDefault)
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "set the target used outside workspaces")
	cmd.Flags().BoolVar(&workspaceDefault, "workspace-default", false, "write default_target into the shared workspace file")
	return cmd
}

func runTargetUse(cmd *cobra.Command, name string, global, workspaceDefault bool) error {
	if global && workspaceDefault {
		return fmt.Errorf("--global and --workspace-default are mutually exclusive")
	}
	tc, err := loadTargetContext(cmd)
	if err != nil {
		return err
	}
	t, ok := tc.lookup(name)
	if !ok {
		return fmt.Errorf("unknown target %q; known targets: %s", name, knownOrNone(tc))
	}
	out := cmd.OutOrStdout()
	switch {
	case workspaceDefault:
		if tc.ws == nil || t.Scope != scopeWorkspace {
			return fmt.Errorf("--workspace-default needs a target defined in the workspace file")
		}
		if eerr := editWorkspaceFile(tc.ws.File, func(e *workspaceEditor) error { e.setDefaultTarget(name); return nil }); eerr != nil {
			return eerr
		}
		_, err = fmt.Fprintf(out, "%s is now the default target of %s\n", name, tc.ws.File)
		return err
	case tc.ws != nil && !global:
		path, perr := updateStore(cmd, func(s *config.UserStore) { s.SetWorkspaceTarget(tc.ws.Root, name) })
		if perr != nil {
			return perr
		}
		_, err = fmt.Fprintf(out, "Using target %s (%s) in workspace %s (saved in %s)\n", name, t.Server, tc.ws.Root, path)
		return err
	default:
		if t.Scope != scopeUser {
			return fmt.Errorf("target %q is defined in %s; outside that workspace it does not exist, so it cannot be the global target", name, tc.ws.File)
		}
		path, perr := updateStore(cmd, func(s *config.UserStore) { s.CurrentTarget = name })
		if perr != nil {
			return perr
		}
		_, err = fmt.Fprintf(out, "Using target %s (%s) (saved in %s)\n", name, t.Server, path)
		return err
	}
}

// updateStore applies change to the config file the command writes to and
// returns its path.
func updateStore(cmd *cobra.Command, change func(*config.UserStore)) (string, error) {
	path, err := sessionConfigPath(cmd)
	if err != nil {
		return "", err
	}
	return path, config.UpdateUserStore(path, func(s *config.UserStore) error {
		change(s)
		return nil
	})
}

// knownOrNone lists the known targets, or says there are none.
func knownOrNone(tc *targetContext) string {
	if known := tc.knownTargets(); len(known) > 0 {
		return strings.Join(known, ", ")
	}
	return "(none; add one with `dexaflow target add <name> <url>`)"
}

func newTargetListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the targets, marking the one in use.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tc, err := loadTargetContext(cmd)
			if err != nil {
				return err
			}
			return printTargets(cmd, tc)
		},
	}
}

// targetRow is one line of `target list`.
type targetRow struct {
	t       *activeTarget
	session bool
}

// targetRows lists every target the context resolves, workspace targets first;
// a user target hidden by a workspace target of the same name is left out.
func targetRows(tc *targetContext) []targetRow {
	names := tc.knownTargets()
	rows := make([]targetRow, 0, len(names))
	for _, name := range names {
		t, _ := tc.lookup(name)
		_, saved := tc.store.Session(t.Server)
		if !saved && tc.store.Token != "" && sameServer(tc.store.ServerURL, t.Server) {
			saved = true
		}
		rows = append(rows, targetRow{t: t, session: saved})
	}
	return rows
}

func printTargets(cmd *cobra.Command, tc *targetContext) error {
	out := cmd.OutOrStdout()
	rows := targetRows(tc)
	if len(rows) == 0 {
		_, err := fmt.Fprintln(out, "No targets. Add one with:  dexaflow target add <name> <server-url>")
		return err
	}
	active, aerr := tc.selectTarget(cmd)
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	//nolint:errcheck // tabwriter buffers; Flush reports the write error.
	fmt.Fprintln(tw, "  NAME\tSERVER\tREGISTRY\tSCOPE\tSIGNED IN")
	for _, r := range rows {
		mark := " "
		if active != nil && active.Name == r.t.Name {
			mark = "*"
		}
		signed := "no"
		if r.session {
			signed = "yes"
		}
		//nolint:errcheck // see above.
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n", mark, r.t.Name, r.t.Server, dashIfEmpty(r.t.Defaults.Registry), r.t.Scope, signed)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return printActive(out, active, aerr)
}

// printActive says which target is in use and why, or why none is.
func printActive(out io.Writer, active *activeTarget, err error) error {
	switch {
	case err != nil:
		_, werr := fmt.Fprintf(out, "\nwarning: %v\n", err)
		return werr
	case active == nil:
		_, werr := fmt.Fprintln(out, "\nNo target selected: commands use --server, DEXAFLOW_SERVER_URL or server_url from the config file.")
		return werr
	default:
		_, werr := fmt.Fprintf(out, "\nIn use: %s (%s)\n", active.Name, active.Origin)
		return werr
	}
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newTargetRemoveCommand() *cobra.Command {
	var global, logout bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a target (from the workspace file, or your config with --global).",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTargetRemove(cmd, args[0], global, logout)
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "remove the target from your config file even inside a workspace")
	cmd.Flags().BoolVar(&logout, "logout", false, "also forget the session saved for the target's server")
	return cmd
}

func runTargetRemove(cmd *cobra.Command, name string, global, logout bool) error {
	tc, err := loadTargetContext(cmd)
	if err != nil {
		return err
	}
	var server, where string
	if tc.ws != nil && !global {
		server, where, err = removeWorkspaceTarget(tc.ws, name)
	} else {
		server, where, err = removeUserTarget(cmd, name)
	}
	if err != nil {
		return err
	}
	// The user's own selection for this workspace must not keep naming a
	// target that no longer exists.
	forgot := false
	if _, uerr := updateStore(cmd, func(s *config.UserStore) {
		if tc.ws != nil && s.WorkspaceTarget(tc.ws.Root) == name && where == tc.ws.File {
			s.SetWorkspaceTarget(tc.ws.Root, "")
		}
		if logout {
			forgot = s.RemoveSession(server)
			// The single-server pair, when it is for the same server, is the
			// same credential under its older name.
			if s.Token != "" && sameServer(s.ServerURL, server) {
				s.ServerURL, s.Token = "", ""
				forgot = true
			}
		}
	}); uerr != nil {
		return uerr
	}
	out := cmd.OutOrStdout()
	if _, werr := fmt.Fprintf(out, "Removed target %s from %s\n", name, where); werr != nil {
		return werr
	}
	if forgot {
		_, err = fmt.Fprintf(out, "Forgot the session saved for %s\n", server)
	}
	return err
}

// removeWorkspaceTarget removes the target from the workspace file, and its
// default_target when it named it.
func removeWorkspaceTarget(ws *workspaceLocation, name string) (server, where string, err error) {
	t := ws.Spec.Targets[name]
	if t == nil {
		return "", "", fmt.Errorf("no target %q in %s; known targets: %s", name, ws.File, strings.Join(ws.Spec.targetNames(), ", "))
	}
	err = editWorkspaceFile(ws.File, func(e *workspaceEditor) error {
		e.removeTarget(name)
		if ws.Spec.DefaultTarget == name {
			e.setDefaultTarget("")
		}
		return nil
	})
	return t.Server, ws.File, err
}

// removeUserTarget removes the target from the user's config file, and its
// current_target when it named it.
func removeUserTarget(cmd *cobra.Command, name string) (server, where string, err error) {
	path, err := sessionConfigPath(cmd)
	if err != nil {
		return "", "", err
	}
	err = config.UpdateUserStore(path, func(s *config.UserStore) error {
		t, ok := s.Target(name)
		if !ok {
			return fmt.Errorf("no target %q in %s", name, path)
		}
		server = t.Server
		s.RemoveTarget(name)
		if s.CurrentTarget == name {
			s.CurrentTarget = ""
		}
		return nil
	})
	return server, path, err
}
