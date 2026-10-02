// Package cli implements the dexaflow command-line interface.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	root.PersistentFlags().String("config", "", "config file path (default ~/.leoflow/config.yaml)")
	root.PersistentFlags().String("log-level", "", "log level: debug, info, warn, error")
	root.PersistentFlags().String("server-url", "", "control plane API base URL")

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

	authoring := []*cobra.Command{newInitCommand(), newValidateCommand(), newCompileCommand(), newBuildCommand(), newPushCommand(), newDeployCommand()}
	runtime := []*cobra.Command{newLiteCommand(), newServerCommand()}
	inspection := []*cobra.Command{newDagsCommand(), newRunsCommand(), newAuthCommand()}
	operations := []*cobra.Command{newAdminCommand(), newConnectionsCommand(), newVariablesCommand()}
	lifecycle := []*cobra.Command{newSetupCommand(), newDoctorCommand(), newDBCommand(), newUninstallCommand(), newVersionCommand()}

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

// Execute runs the root command and returns a process exit code.
func Execute() int {
	// DEXAFLOW_* and the pre-rename LEOFLOW_* names are interchangeable, and
	// processes this CLI starts (server, agent, parser) inherit both.
	envReport := envcompat.MirrorProcess()
	if stderrIsTerminal() {
		for _, note := range envReport.Notes() {
			fmt.Fprintln(os.Stderr, "note: "+note)
		}
	}
	// Only on an interactive terminal: scripts that run `leoflow version | head -1`
	// or parse stderr must see exactly what they saw before the rename.
	if notice := legacyNameNotice(os.Args[0]); notice != "" && stderrIsTerminal() {
		fmt.Fprintln(os.Stderr, notice)
	}
	if err := NewRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
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
