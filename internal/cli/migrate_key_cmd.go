package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// newMigrateKeyCommand builds `dexaflow lite migrate-key` (ADR 0065).
//
// It is the only path that moves a Lite install's stored secrets between keys.
// `dexaflow lite`, setup, uninstall, reset-password, backup and restore never
// re-encrypt a row and never add or remove a key on Lite's behalf.
func newMigrateKeyCommand() *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "migrate-key",
		Short: "Move stored connection secrets off the key published in this repository, onto a key only this install has.",
		Long: "migrate-key re-encrypts every stored connection secret of this Lite install onto a key of its\n" +
			"own and records it in ~/.dexaflow/config.yaml. Run it when `dexaflow lite` warns that your secrets\n" +
			"are under the published key, that a key migration has not finished, or that some secrets are under\n" +
			"the published key and cannot be read.\n\n" +
			"Stop `dexaflow lite` first: the command refuses while a Lite server runs against any datastore of\n" +
			"this install. It scans every datastore the install has (the managed Postgres and the Docker one),\n" +
			"records the new key next to every old one before touching a row, moves the rows in one verified\n" +
			"transaction per datastore, and drops the old keys only after re-checking every datastore.\n\n" +
			"If it is interrupted at any point, run it again: it resumes. Afterwards config.yaml is the only copy\n" +
			"of the key, so back it up (`dexaflow lite backup`).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMigrateKey(cmd, dryRun, yes)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "scan and report what would be done; write nothing")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func runMigrateKey(cmd *cobra.Command, dryRun, yes bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home dir: %w", err)
	}
	if inv := invokingUserHome(); inv != "" && inv != home {
		return fmt.Errorf("run `dexaflow lite migrate-key` as the user who runs `dexaflow lite` (without sudo): this process resolves its datastores under %s but its config under %s", home, inv)
	}
	stateDir := stateDirIn(home)
	dss, cleanup, err := bringUpKeyDatastores(cmd, stateDir)
	defer cleanup()
	if err != nil {
		return err
	}
	r := &migrateKeyRun{
		out: cmd.OutOrStdout(), stateDir: stateDir, datastores: dss,
		dryRun: dryRun, yes: yes,
		confirm: func() bool { return confirmDestructive(cmd) },
		crashAt: migrateKeyCrashHook,
	}
	return r.run(cmdContext(cmd))
}

// migrateKeyCrashHook is a no-op in every build but the crash-injection one
// (build tag crashhooks), which SIGKILLs the process at a named step.
var migrateKeyCrashHook = func(string) {}

// bringUpKeyDatastores finds every datastore this install has, on disk, not
// the one `--postgres auto` would pick on this run (gap 9), and brings each up
// without starting a server. The returned cleanup leaves each as it was found:
// it stops only what it started.
//
// A datastore that exists but cannot be brought up is a refusal before step 1:
// a predecessor can be dropped only once every datastore that might hold rows
// under it was scanned.
func bringUpKeyDatastores(cmd *cobra.Command, stateDir string) ([]keyDatastore, func(), error) {
	var dss []keyDatastore
	var stops []func()
	cleanup := func() {
		for i := len(stops) - 1; i >= 0; i-- {
			stops[i]()
		}
	}
	ctx := cmdContext(cmd)
	if ds, stop, ok, err := bringUpManagedForKeys(ctx, cmd, stateDir); err != nil {
		return nil, cleanup, err
	} else if ok {
		dss = append(dss, ds)
		stops = append(stops, stop)
	}
	ds, stop, ok, err := bringUpDockerForKeys(ctx, cmd, stateDir)
	if err != nil {
		return nil, cleanup, err
	}
	if ok {
		dss = append(dss, ds)
		stops = append(stops, stop)
	}
	if len(dss) == 0 {
		devPrintln(cmd.OutOrStdout(), "  no Lite datastore found on this machine; only config.yaml is updated.")
	}
	return dss, cleanup, nil
}

// bringUpManagedForKeys covers the managed cluster when
// ~/.dexaflow/pgdata/PG_VERSION exists, starting it when it is not running.
func bringUpManagedForKeys(ctx context.Context, cmd *cobra.Command, stateDir string) (ds keyDatastore, stop func(), found bool, err error) {
	noop := func() {}
	dataDir := filepath.Join(stateDir, "pgdata")
	if !fileExists(filepath.Join(dataDir, "PG_VERSION")) {
		return keyDatastore{}, noop, false, nil
	}
	name := "the managed datastore (" + dataDir + ")"
	started, serr := startManagedPostgres(ctx, cmd)
	if serr != nil {
		return keyDatastore{}, noop, false, fmt.Errorf("refusing to migrate: %s exists but cannot be started (%w); no file and no row was written", name, serr)
	}
	//nolint:contextcheck // the stop runs after the command, with its own context
	return keyDatastore{name: name, dsn: socketDSNs(dataDir).database}, managedCleanup(started, func() { stopManagedPostgres(cmd) }), true, nil
}

// bringUpDockerForKeys covers this install's Docker volume. With Docker off it
// cannot tell whether the volume exists, so a recorded Docker port (written
// only by a Lite run on the Docker datastore) is enough to refuse.
func bringUpDockerForKeys(ctx context.Context, cmd *cobra.Command, stateDir string) (ds keyDatastore, stop func(), found bool, err error) {
	noop := func() {}
	devDir := filepath.Join(stateDir, "dev")
	_, portRecorded := readPort(filepath.Join(devDir, "db-port"))
	if !dockerUsable() {
		if portRecorded {
			return keyDatastore{}, noop, false, fmt.Errorf("refusing to migrate: this install has used the Docker datastore (%s), and Docker is not available to scan it; start Docker and run this again (no file and no row was written)", filepath.Join(devDir, "db-port"))
		}
		return keyDatastore{}, noop, false, nil
	}
	project := projectName(stateDir)
	volume := project + "_leoflow_pgdata"
	// `volume ls` with an exact-name filter prints nothing when the volume is
	// absent and fails only when Docker itself does; a failure is a refusal,
	// never "no datastore here".
	listed, lerr := exec.CommandContext(ctx, "docker", "volume", "ls", "-q", "--filter", "name=^"+volume+"$").Output() //nolint:gosec // fixed args, derived volume name
	if lerr != nil {
		return keyDatastore{}, noop, false, fmt.Errorf("refusing to migrate: could not ask Docker whether this install's volume %s exists (%w); no file and no row was written", volume, lerr)
	}
	if strings.TrimSpace(string(listed)) == "" {
		return keyDatastore{}, noop, false, nil
	}
	port := devDBPort(devDir)
	name := fmt.Sprintf("the Docker datastore (volume %s, localhost:%d)", volume, port)
	wasRunning := dockerPostgresRunning(ctx, project)
	cf, cerr := resolveComposeFile("")
	if cerr != nil {
		return keyDatastore{}, noop, false, cerr
	}
	if !wasRunning {
		if uerr := devComposeUp(ctx, cmd, devOptions{composeFile: cf}, "postgres"); uerr != nil {
			return keyDatastore{}, noop, false, fmt.Errorf("refusing to migrate: %s exists but cannot be started (%w); no file and no row was written", name, uerr)
		}
	}
	stop = func() { //nolint:contextcheck // runs after the command, with its own context
		if wasRunning {
			return
		}
		c := exec.CommandContext(context.Background(), "docker", "compose", "-f", cf, "stop", "postgres") //nolint:gosec // fixed args, managed compose path
		c.Env = composeEnv()
		_ = c.Run() //nolint:errcheck // best effort: leave the datastore as it was found
	}
	return keyDatastore{name: name, dsn: tcpDSNs(port).database}, stop, true, nil
}

// dockerUsable reports whether the docker CLI is installed and its daemon
// answers.
func dockerUsable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return dockerResponsive()
}

// dockerPostgresRunning reports whether this install's Postgres container runs.
func dockerPostgresRunning(ctx context.Context, project string) bool {
	out, err := exec.CommandContext(ctx, "docker", "ps", "-q", //nolint:gosec // fixed args, derived project name
		"--filter", "label=com.docker.compose.project="+project,
		"--filter", "label=com.docker.compose.service=postgres").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}
