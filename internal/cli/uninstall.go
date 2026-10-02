package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
)

// newUninstallCommand removes the Dexaflow installation (~/.dexaflow). It confirms
// first (unless --yes); --purge additionally removes the DAG workspace and the
// Docker datastore volumes.
func newUninstallCommand() *cobra.Command {
	var yes, purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the Dexaflow installation (~/.dexaflow).",
		Long: "uninstall removes the managed Dexaflow home (~/.dexaflow): the binaries, config, " +
			"managed Python, Monaco assets, and local dev state. It does NOT remove your DAG " +
			"workspace or your datastore (the managed Postgres data in ~/.dexaflow/pgdata and this " +
			"install's Docker volume) unless you pass --purge — so a reinstall keeps your data. It " +
			"asks for confirmation unless --yes is given. (To upgrade instead, just re-run install.sh " +
			"— it replaces the binaries and keeps your config.)",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUninstall(cmd, yes, purge)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove the DAG workspace and Docker datastore volumes (destructive)")
	return cmd
}

func runUninstall(cmd *cobra.Command, yes, purge bool) error {
	out := cmd.OutOrStdout()
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home dir: %w", err)
	}
	root := stateDirIn(home)
	if _, serr := os.Stat(root); errors.Is(serr, os.ErrNotExist) {
		devPrintf(out, "Nothing to remove: %s does not exist.\n", root)
		return nil
	}

	// Read the workspace path before deleting, so --purge can remove it too.
	workspace := ""
	if c, lerr := config.Load(filepath.Join(root, "config.yaml"), nil); lerr == nil {
		workspace = c.Workspace
	}

	binDir := installBinDir()
	devPrintf(out, "This will remove the Dexaflow installation:\n  %s  (config, managed Python, Monaco, sources)\n", root)
	if binDir != "" {
		devPrintf(out, "  the leoflow binaries in %s\n", binDir)
	}
	if purge {
		if workspace != "" {
			devPrintf(out, "  %s  (your DAG workspace)\n", workspace)
		}
		devPrintln(out, "  your datastore: the managed Postgres data (~/.dexaflow/pgdata) AND this install's Docker volume")
	} else {
		devPrintln(out, "  (keeping your datastore — the managed pgdata and the Docker volume — and your DAG workspace; pass --purge to remove them)")
	}

	// The datastore survives a plain uninstall; the key that decrypts its
	// connection secrets does not, because it lives in the config.yaml this
	// command deletes (#486). Say so before the confirmation, not after: the
	// user is about to make their preserved data unreadable while this command's
	// own help says a reinstall keeps it.
	if strandsDatastoreKey(configFileSecrets(filepath.Join(root, "config.yaml")).secretKey != "", !purge) {
		devPrintln(out, "")
		devPrintln(out, "  WARNING: your datastore is kept, but the key that decrypts its connection")
		devPrintln(out, "           secrets is in the config being removed. A reinstall generates a NEW")
		devPrintln(out, "           key, and the stored passwords will not be readable.")
		devPrintf(out, "           Copy `secret_key` out of %s first if you want them back.\n",
			filepath.Join(root, "config.yaml"))
	}

	if !yes && !confirmDestructive(cmd) {
		devPrintln(out, "aborted.")
		return nil
	}

	if rerr := removeLeoflowHome(cmd, root, purge); rerr != nil {
		return rerr
	}
	devPrintf(out, "✓ removed the Dexaflow install at %s\n", root)
	if !purge {
		devPrintln(out, "  (kept your datastore for a future reinstall — `dexaflow uninstall --purge` removes it)")
	}
	// Remove the binaries too — install.sh places them on a PATH dir (e.g.
	// /usr/local/bin), NOT under ~/.dexaflow, so removing the home alone left a
	// working `leoflow` behind.
	removeBinariesIn(out, installBinDir())
	if purge && workspace != "" {
		if rerr := os.RemoveAll(workspace); rerr != nil {
			devPrintf(out, "  ! could not remove workspace %s: %v\n", workspace, rerr)
		} else {
			devPrintf(out, "✓ removed workspace %s\n", workspace)
		}
	}
	devPrintln(out, "Done. If install.sh added a 'leoflow' PATH line to your shell profile, remove it.")
	return nil
}

// installBinDir is the directory the running leoflow binary lives in — where
// install.sh placed the binaries (/usr/local/bin, ~/.local/bin, or ~/.dexaflow/bin).
func installBinDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// removeBinariesIn deletes the dexaflow binaries from dir, and the leoflow-named
// entry points older installs left there, with the CLI last (it is the running
// process; on Linux unlinking a running binary is safe). A removal failure
// (e.g. /usr/local/bin without sudo) is reported, not fatal, so ~/.dexaflow is still
// cleaned.
func removeBinariesIn(out io.Writer, dir string) {
	if dir == "" {
		return
	}
	for _, name := range []string{
		"dexaflow-server", "dexaflow-agent", "dexaflow-mcp",
		"leoflow-server", "leoflow-agent", "leoflow-mcp",
		"leoflow", "dexaflow",
	} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if err := os.Remove(p); err != nil {
			devPrintf(out, "  ! could not remove %s: %v (remove it manually, e.g. `sudo rm %s`)\n", p, err, p)
		} else {
			devPrintf(out, "✓ removed %s\n", p)
		}
	}
}

// confirmDestructive reads a yes/no from stdin; anything but yes/y (and any EOF
// on a non-interactive stdin) aborts, so a destructive action is never taken by
// accident. Shared by every command that cannot be undone.
func confirmDestructive(cmd *cobra.Command) bool {
	devPrintf(cmd.OutOrStdout(), "Type 'yes' to confirm (or re-run with --yes): ")
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "yes" || answer == "y"
}

// removeLeoflowHome removes the Dexaflow home, stopping a running managed Postgres
// first (so no orphaned process points at a half-removed data dir). With purge it
// also drops this install's Docker volume and removes the datastore; without it,
// the datastore (managed pgdata / the Docker volume) is preserved for a reinstall.
func removeLeoflowHome(cmd *cobra.Command, root string, purge bool) error {
	// root may be the ~/.dexaflow link to a pre-rename ~/.dexaflow
	// (config.HomeDirIn). Work on the real directory, then drop the link, so the
	// data goes and no dangling link stays behind.
	if target, err := filepath.EvalSymlinks(root); err == nil && target != root {
		if rerr := removeLeoflowHome(cmd, target, purge); rerr != nil {
			return rerr
		}
		if _, serr := os.Stat(target); errors.Is(serr, os.ErrNotExist) {
			if lerr := os.Remove(root); lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
				return fmt.Errorf("removing %s: %w", root, lerr)
			}
		}
		return nil
	}
	stopManagedPostgres(cmd)
	if purge {
		// Best-effort: stop the Docker datastore and drop this install's volume
		// before deleting the compose file that defines it.
		composeDownVolumes(cmd, filepath.Join(root, "docker-compose.yaml"))
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("removing %s: %w", root, err)
		}
		return nil
	}
	if err := removeHomeExcept(root, "pgdata"); err != nil {
		return fmt.Errorf("removing %s: %w", root, err)
	}
	return nil
}

// removeHomeExcept deletes everything under root except the entry named keep — the
// datastore (managed pgdata), preserved on a plain uninstall so a reinstall at the
// same HOME reconnects to its data (symmetric with the Docker volume, which a plain
// uninstall also leaves intact). If keep is absent (e.g. a Docker-only install),
// root is emptied and removed entirely.
func removeHomeExcept(root, keep string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	kept := false
	for _, e := range entries {
		if e.Name() == keep {
			kept = true
			continue
		}
		if rerr := os.RemoveAll(filepath.Join(root, e.Name())); rerr != nil {
			return rerr
		}
	}
	if !kept {
		return os.Remove(root)
	}
	return nil
}

// composeDownVolumes best-effort stops the datastores and removes their volumes
// via the managed compose file, if both Docker and the file are present.
func composeDownVolumes(cmd *cobra.Command, composeFile string) {
	if _, err := os.Stat(composeFile); err != nil {
		return
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return
	}
	c := exec.CommandContext(cmdContext(cmd), "docker", "compose", "-f", composeFile, "down", "-v") //nolint:gosec // fixed args, managed compose path
	// Run under this install's project name so `down -v` removes exactly this
	// install's container and volume, never a co-resident user's.
	c.Env = composeEnv()
	c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		// Best-effort: a missing/already-down stack is fine; the files are removed next.
		devPrintf(cmd.OutOrStdout(), "  ! docker compose down (datastores may already be gone): %v\n", err)
	}
}

// strandsDatastoreKey reports whether this uninstall leaves a datastore behind
// that nothing can decrypt: a per-install key exists, and the data outlives the
// config holding it.
//
// Copying the key next to the datastore was tried and rejected. It put the only
// copy of the key inside the very artifact the threat model names, a file that
// travels in backups and synced directories, which re-opens what the key change
// exists to close. Warning and letting the user take the copy keeps the decision
// where the context is.
func strandsDatastoreKey(hasPerInstallKey, keepsDatastore bool) bool {
	return hasPerInstallKey && keepsDatastore
}
