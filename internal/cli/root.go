// Package cli implements the dexaflow command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/version"
)

// NewRootCommand builds the root dexaflow command with its global flags and
// subcommands.
func NewRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "dexaflow",
		Short:         "Dexaflow is a GitOps-first, container-native workflow orchestrator.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// Setting Version makes cobra accept the `--version` flag, matching the
		// companion binaries (#598). The template prints exactly what the
		// `version` subcommand does (info.String()), so the flag and the
		// subcommand are interchangeable.
		Version: version.Get().String(),
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.PersistentFlags().String("config", "", "config file path (default ~/.dexaflow/config.yaml)")
	root.PersistentFlags().String("log-level", "", "log level: debug, info, warn, error")
	root.PersistentFlags().String("server-url", "", "control plane API base URL")
	root.PersistentFlags().String("target", "", "`name` of the control plane to use (default: DEXAFLOW_TARGET, then the target selected for the workspace; see dexaflow target list)")

	// Cobra command groups (issue #D1, dogfood audit #212): split the 17
	// commands into four navigable sections so new users see "init → run"
	// as one block, not as one long alphabetised list.
	root.AddGroup(
		&cobra.Group{ID: "authoring", Title: "Authoring loop (write → check → package → push):"},
		&cobra.Group{ID: "runtime", Title: "Runtime (start the control plane):"},
		&cobra.Group{ID: "inspection", Title: "Inspection (query DAGs / runs / auth):"},
		&cobra.Group{ID: "operations", Title: "Operations (operate a running control plane):"},
		&cobra.Group{ID: "lifecycle", Title: "Lifecycle (install, configure, repair, retire):"},
	)

	authoring := []*cobra.Command{newWorkspaceCommand(), newNewCommand(), newInitCommand(), newValidateCommand(), newCompileCommand(), newBuildCommand(), newPushCommand(), newDeployCommand(), newSyncCommand(), newRegistryCommand()}
	runtime := []*cobra.Command{newLiteCommand(), newServerCommand()}
	inspection := []*cobra.Command{newStatusCommand(), newDagsCommand(), newRunsCommand(), newAuthCommand(), newTargetCommand()}
	operations := []*cobra.Command{newAdminCommand(), newPromoteCommand(), newRollbackCommand(), newConnectionsCommand(), newVariablesCommand()}
	lifecycle := []*cobra.Command{newSetupCommand(), newCICommand(), newDoctorCommand(), newDBCommand(), newUninstallCommand(), newVersionCommand()}

	for _, c := range authoring {
		c.GroupID = "authoring"
		root.AddCommand(c)
	}
	for _, c := range runtime {
		c.GroupID = "runtime"
		root.AddCommand(c)
	}
	for _, c := range inspection {
		c.GroupID = "inspection"
		root.AddCommand(c)
	}
	for _, c := range operations {
		c.GroupID = "operations"
		root.AddCommand(c)
	}
	for _, c := range lifecycle {
		c.GroupID = "lifecycle"
		root.AddCommand(c)
	}

	// gen-docs is a maintenance helper — leave ungrouped so it does not
	// clutter the user-facing sections.
	root.AddCommand(newGenDocsCommand())

	return root
}

// Execute runs the root command and returns a process exit code: 0 on
// success, the code an error carries (a followed run's result, see
// exitRunFailed), or 1 for any other error.
func Execute() int {
	// DEXAFLOW_* and the pre-rename LEOFLOW_* names are interchangeable, and
	// processes this CLI starts (server, agent, parser) inherit both.
	envReport := envcompat.MirrorProcess()
	if stderrIsTerminal() {
		for _, note := range envReport.Notes() {
			fmt.Fprintln(os.Stderr, "note: "+note)
		}
	}
	// Only on an interactive terminal: scripts that run `dexaflow version | head -1`
	// or parse stderr must see exactly what they saw before the rename.
	if notice := legacyNameNotice(os.Args[0]); notice != "" && stderrIsTerminal() {
		fmt.Fprintln(os.Stderr, notice)
	}
	code, err := executeWithSignals(NewRootCommand())
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	return code
}

// executeWithSignals runs root under a context that Ctrl-C (SIGINT) or SIGTERM
// cancels, instead of letting the signal end the process (#1061). A command
// that is waiting on a child (a `docker build` that takes minutes) sees its
// context canceled, the child is stopped through it, and the command returns,
// so its deferred cleanups run: the generated Dockerfile is removed and the
// .dockerignore restored. Commands that install their own handler (lite,
// deploy --watch, runs --follow) derive it from this context and keep working
// as before.
//
// Only the first signal is caught. After it the default handling is back, so a
// second Ctrl-C ends the process at once if a cleanup hangs.
func executeWithSignals(root *cobra.Command) (int, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	return executeRoot(ctx, root)
}

// executeRoot runs root with ctx and returns the process exit code for its
// error, together with the error. A command that failed because ctx was
// canceled (a signal stopped it) exits with exitInterrupted, as a shell does
// for a command Ctrl-C ended, unless its error carries a code of its own.
func executeRoot(ctx context.Context, root *cobra.Command) (int, error) {
	err := root.ExecuteContext(ctx)
	var coded *exitCodeError
	if err != nil && ctx.Err() != nil && !errors.As(err, &coded) {
		return exitInterrupted, err
	}
	return exitCodeOf(err), err
}

// legacyCommandName is the CLI's name before the Dexaflow rename. Installs from
// that time keep a `leoflow` entry point that runs this same binary.
const legacyCommandName = "leoflow"

// legacyNameNotice returns a one-line notice when the CLI was started through
// its pre-rename name, and "" otherwise. The old name keeps working; the notice
// only tells the user what the command is called now.
func legacyNameNotice(argv0 string) string {
	base := filepath.Base(strings.ReplaceAll(argv0, `\`, "/"))
	base = strings.TrimSuffix(base, ".exe")
	if base != legacyCommandName {
		return ""
	}
	return "note: leoflow is now called dexaflow; this command keeps working under both names."
}

// stderrIsTerminal reports whether stderr is attached to a terminal rather than
// a pipe, a file or a CI log.
func stderrIsTerminal() bool {
	fi, err := os.Stderr.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
