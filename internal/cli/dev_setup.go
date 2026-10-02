package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/setup"
)

// devTool is a host dependency `dexaflow lite` needs, with how to install it when
// missing. brewPkg empty means it cannot be auto-installed (e.g. Docker Desktop).
type devTool struct {
	bin     string
	brewPkg string
	hint    string
}

// devTools are the host dependencies for `dexaflow lite` (dev-only). Production
// setup is a separate, later concern (#48/#61).
var devTools = []devTool{
	{bin: "docker", brewPkg: "", hint: "install Docker Desktop and start it: https://www.docker.com/products/docker-desktop"},
	{bin: "k3d", brewPkg: "k3d", hint: "brew install k3d"},
	{bin: "kubectl", brewPkg: "kubectl", hint: "brew install kubectl"},
	{bin: "python3", brewPkg: "python@3.11", hint: "brew install python@3.11"},
}

// brewInstallArgs builds the `brew install <pkg>` argv.
func brewInstallArgs(pkg string) []string { return []string{"install", pkg} }

// newLiteProvisionCommand prepares the local machine for the from-source
// `dexaflow lite` loop: it checks the host dependencies (installing the
// brew-installable ones with --install), ensures the task base image, and
// provisions the isolated local database. Named `provision` to avoid colliding
// with the top-level `dexaflow setup` (the end-user install bootstrap).
func newLiteProvisionCommand() *cobra.Command {
	var install bool
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Check and provision the local deps the from-source `dexaflow lite` loop needs.",
		Long: "provision readies this machine for the from-source `dexaflow lite` loop: it " +
			"checks Docker/k3d/kubectl/python3 (installing the brew-installable ones with " +
			"--install), ensures the task base image, and provisions the isolated local " +
			"database (leoflow_dev). For end users, `dexaflow setup` (the installer) handles " +
			"onboarding; this is the contributor's machine prep.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLiteProvision(cmd, install)
		},
	}
	cmd.Flags().BoolVar(&install, "install", false, "brew-install missing dependencies (where possible)")
	return cmd
}

// runLiteProvision checks/installs host deps, ensures the base image, and provisions
// the dev database. It returns an error if a required dependency is still missing.
func runLiteProvision(cmd *cobra.Command, install bool) error {
	out := cmd.OutOrStdout()
	ctx := cmdContext(cmd)
	devPrintln(out, "▸ dexaflow lite provision (contributor machine prep; end users use `dexaflow setup`)")

	missing := checkDevTools(cmd, install)

	// Fetch the Monaco editor bundle for the Lite web editor (best-effort).
	if home, herr := os.UserHomeDir(); herr == nil {
		if _, mErr := setup.EnsureMonaco(ctx, nil, stateDirIn(home), func(format string, a ...any) {
			devPrintf(out, "  "+format+"\n", a...)
		}); mErr != nil {
			devPrintf(out, "  ! editor assets not fetched (%v) — web editor unavailable until re-run with network\n", mErr)
		}
	}

	// Ensure the task base image (built from source today; pulled once published, #48).
	devPrintf(out, "▸ ensuring task base image %s …\n", devBaseImage)
	if berr := ensureBaseImage(ctx, cmd); berr != nil {
		devPrintf(out, "  ✗ base image: %v\n", berr)
		missing = append(missing, "base image")
	} else {
		devPrintf(out, "  ✓ %s\n", devBaseImage)
	}

	// Provision the isolated dev database (idempotent).
	if derr := ensureDevDatabase(ctx, cmd); derr != nil {
		devPrintf(out, "  ✗ dev database: %v\n", derr)
		missing = append(missing, "dev database")
	} else if merr := devMigrate(cmd); merr != nil {
		devPrintf(out, "  ✗ migrating dev database: %v\n", merr)
		missing = append(missing, "dev database")
	} else {
		devPrintf(out, "  ✓ %s (created + migrated)\n", devDBName)
	}

	if len(missing) > 0 {
		return fmt.Errorf("provision incomplete; unresolved: %v (see hints above)", missing)
	}
	devPrintln(out, "✓ local environment ready — run: dexaflow lite dags/<project>")
	return nil
}

// checkDevTools reports each host tool's presence, optionally brew-installing the
// installable ones, and returns the bins still missing.
func checkDevTools(cmd *cobra.Command, install bool) []string {
	out := cmd.OutOrStdout()
	var missing []string
	for _, t := range devTools {
		if _, err := exec.LookPath(t.bin); err == nil {
			devPrintf(out, "  ✓ %s\n", t.bin)
			continue
		}
		if install && t.brewPkg != "" {
			if _, berr := exec.LookPath("brew"); berr == nil {
				devPrintf(out, "  ▸ installing %s (brew install %s) …\n", t.bin, t.brewPkg)
				ic := exec.CommandContext(cmdContext(cmd), "brew", brewInstallArgs(t.brewPkg)...) //nolint:gosec // fixed brew package
				ic.Stdout, ic.Stderr = out, cmd.ErrOrStderr()
				if ierr := ic.Run(); ierr == nil {
					devPrintf(out, "  ✓ %s (installed)\n", t.bin)
					continue
				}
			}
		}
		devPrintf(out, "  ✗ %s — %s\n", t.bin, t.hint)
		missing = append(missing, t.bin)
	}
	return missing
}
