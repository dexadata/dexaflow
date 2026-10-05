package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dexadata/dexaflow/migrations"
)

// newRestoreCommand wires `dexaflow lite restore`. The mirror of backup:
// extracts the archive, sanity-checks the manifest against the binary's
// embedded schema (refuses if backup is newer than binary — the inverse of
// the upgrade-drift guard), then replays the SQL dump and, only once that
// succeeded, restores config + workspace (#137, ADR 0065 section 8).
func newRestoreCommand() *cobra.Command {
	var input string
	var force bool
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a Lite install from an archive produced by `dexaflow lite backup`.",
		Long: "restore reads a tar.gz produced by `dexaflow lite backup`, validates the " +
			"manifest against this binary (refuses an archive newer than what this " +
			"binary knows about), then replays the datastore SQL and, only once that " +
			"succeeded, restores config and workspace. The config.yaml it replaces is kept " +
			"as ~/.dexaflow/config.yaml.pre-restore until a scan of every datastore finds every " +
			"stored secret under the restored keys: the next `dexaflow lite` when the install has one " +
			"datastore, else `dexaflow lite migrate-key`. " +
			"The archive's config is restored as it is: an archive from before this install " +
			"had a key of its own restores onto the published key, and `dexaflow lite " +
			"migrate-key` moves it.\n\n" +
			"By default refuses to overwrite a non-empty ~/.dexaflow; pass --force to confirm.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRestore(cmd, input, force)
		},
	}
	cmd.Flags().StringVarP(&input, "input", "i", "", "path to the archive (required)")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing ~/.dexaflow install")
	_ = cmd.MarkFlagRequired("input") //nolint:errcheck // Cobra returns nil for a known flag name; the error path is unreachable
	return cmd
}

func runRestore(cmd *cobra.Command, input string, force bool) error {
	out := cmd.OutOrStdout()
	home := invokingUserHome()
	if home == "" {
		return fmt.Errorf("could not resolve the user home directory")
	}
	leoflowHome := stateDirIn(home)

	devPrintf(out, "▸ reading manifest from %s …\n", input)
	archive, err := readBackupArchive(input)
	if err != nil {
		return err
	}

	embedded, lerr := migrations.Latest()
	if lerr != nil {
		return fmt.Errorf("reading embedded schema version: %w", lerr)
	}
	homeHasData := leoflowHomeHasData(leoflowHome)
	if serr := decideRestoreSafe(archive.Manifest.SchemaVersion, embedded, homeHasData, force); serr != nil {
		return serr
	}

	devPrintf(out, "  archive: dexaflow=%s schema=%d created=%s\n",
		archive.Manifest.LeoflowVersion, archive.Manifest.SchemaVersion,
		archive.Manifest.CreatedAt.Format("2006-01-02 15:04:05 UTC"))

	if err := os.MkdirAll(leoflowHome, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", leoflowHome, err)
	}
	release, lerr := lockConfigDir(leoflowHome, configLockExclusive, false)
	if lerr != nil {
		return lerr
	}
	defer release()

	// Replay the datastore FIRST and write the config only after the replay
	// succeeded (ADR 0065 section 8). The replay is one psql transaction that
	// can fail (disk full, a schema mismatch); when it does, the datastore is
	// unchanged and still under the current key, so the config that records
	// that key must still be there.
	if len(archive.Dump) == 0 {
		return fmt.Errorf("archive has no datastore.sql; cannot restore datastore")
	}
	devPrintln(out, "▸ replaying datastore via psql …")
	if err := psqlRestore(cmd.Context(), leoflowHome, archive.Dump); err != nil {
		return fmt.Errorf("%w; config.yaml was left as it was", err)
	}
	if err := restoreConfigFiles(out, leoflowHome, archive); err != nil {
		return err
	}

	if len(archive.Workspace) > 0 {
		workspaceDir, werr := workspaceDirFromConfig(archive.Config)
		if werr != nil {
			return fmt.Errorf("resolving workspace dir from restored config: %w", werr)
		}
		if err := restoreWorkspaceTree(workspaceDir, archive.Workspace); err != nil {
			return err
		}
		devPrintf(out, "✓ workspace restored to %s (%d files)\n", workspaceDir, len(archive.Workspace))
	}

	devPrintln(out, "✓ restore complete; run `dexaflow lite` to start.")
	return nil
}

// psqlRestore replays a dump into the Lite database; a variable so tests can
// make the replay fail or succeed without a managed Postgres.
var psqlRestore = runPsqlRestore

// restoreConfigFiles writes the archive's config.yaml and setup.json. The
// config is written as it is: restore never adds or removes a key on Lite's
// behalf, so an archive from before per-install keys restores as a Legacy
// install, which `dexaflow lite migrate-key` then moves. It is written
// atomically at 0600 (os.WriteFile kept a loose mode and could leave a torn
// file, gap 4), and the config it replaces is kept as config.yaml.pre-restore
// until a scan covering every datastore on disk finds every stored secret
// under the restored keys (removePreRestore, migrateKeyRun.removePreRestore).
func restoreConfigFiles(out io.Writer, leoflowHome string, archive archiveContents) error {
	cfgPath := filepath.Join(leoflowHome, "config.yaml")
	if len(archive.Config) > 0 {
		if cur, err := os.ReadFile(cfgPath); err == nil { //nolint:gosec // the user's own config
			pre := filepath.Join(leoflowHome, preRestoreName)
			if werr := writeFileAtomicWith(pre, cur, atomicOpts{ownerFrom: cfgPath}); werr != nil {
				return fmt.Errorf("keeping the current config.yaml before replacing it: %w", werr)
			}
			devPrintf(out, "✓ kept the config.yaml this restore replaces at %s (removed once every stored secret is found under the restored keys: by the next `dexaflow lite` with one datastore, else by `dexaflow lite migrate-key`)\n", pre)
		}
		if err := writeFileAtomic(cfgPath, archive.Config); err != nil {
			return fmt.Errorf("restoring config.yaml: %w", err)
		}
		devPrintln(out, "✓ config.yaml restored")
	}
	if len(archive.Setup) > 0 {
		if err := writeFileAtomic(filepath.Join(leoflowHome, "setup.json"), archive.Setup); err != nil {
			return fmt.Errorf("restoring setup.json: %w", err)
		}
		devPrintln(out, "✓ setup.json restored")
	}
	return nil
}

// archiveContents is the structured view a restore reads out of a backup
// tarball. Splitting fields lets the call site decide what to apply (e.g.
// skip the workspace if it is empty) without juggling six return values.
type archiveContents struct {
	Manifest  backupManifest
	Dump      []byte
	Config    []byte
	Setup     []byte
	Workspace map[string][]byte
}

// readBackupArchive walks the tarball once and returns each known artifact.
// MANIFEST is the only required entry; an archive missing it (or with a
// zero-valued one) is refused so a corrupt or hand-rolled bundle cannot
// half-restore. workspace/<rel> entries land in a path→bytes map so the
// restore can rebuild the tree later in the user-resolved workspace dir.
func readBackupArchive(path string) (archiveContents, error) {
	f, err := os.Open(path) //nolint:gosec // user-chosen input path
	if err != nil {
		return archiveContents{}, fmt.Errorf("opening archive: %w", err)
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // best-effort close
	gz, err := gzip.NewReader(f)
	if err != nil {
		return archiveContents{}, fmt.Errorf("reading gzip header: %w", err)
	}
	defer func() { _ = gz.Close() }() //nolint:errcheck // best-effort gzip close
	tr := tar.NewReader(gz)
	out := archiveContents{Workspace: map[string][]byte{}}
	var manifestData []byte
	for {
		hdr, herr := tr.Next()
		if herr == io.EOF {
			break
		}
		if herr != nil {
			return archiveContents{}, fmt.Errorf("reading archive: %w", herr)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		data, rerr := io.ReadAll(tr)
		if rerr != nil {
			return archiveContents{}, fmt.Errorf("reading %s: %w", hdr.Name, rerr)
		}
		switch {
		case hdr.Name == "MANIFEST.json":
			manifestData = data
		case hdr.Name == "config.yaml":
			out.Config = data
		case hdr.Name == "setup.json":
			out.Setup = data
		case hdr.Name == "datastore.sql":
			out.Dump = data
		case strings.HasPrefix(hdr.Name, "workspace/"):
			out.Workspace[strings.TrimPrefix(hdr.Name, "workspace/")] = data
		}
	}
	if len(manifestData) == 0 {
		return archiveContents{}, fmt.Errorf("archive has no MANIFEST.json; is this a dexaflow backup?")
	}
	manifest, merr := unmarshalManifest(manifestData)
	if merr != nil {
		return archiveContents{}, merr
	}
	out.Manifest = manifest
	return out, nil
}

// leoflowHomeHasData reports whether ~/.dexaflow already contains an install:
// any file inside qualifies (config.yaml, pgdata, etc.). The check is the
// guard the restore decision uses to refuse a destructive overwrite.
func leoflowHomeHasData(leoflowHome string) bool {
	info, err := os.Stat(leoflowHome)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(leoflowHome)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// workspaceDirFromConfig reads the workspace path out of a config.yaml
// blob, defaulting to ~/leoflow-projects when the file does not say. The
// helper is split so it can be tested without writing to disk.
func workspaceDirFromConfig(configData []byte) (string, error) {
	if len(configData) == 0 {
		return defaultWorkspaceDir(), nil
	}
	var c struct {
		Workspace string `yaml:"workspace"`
	}
	if err := yaml.Unmarshal(configData, &c); err != nil {
		return "", err
	}
	if c.Workspace == "" {
		return defaultWorkspaceDir(), nil
	}
	return c.Workspace, nil
}

func defaultWorkspaceDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "leoflow-projects"
	}
	return filepath.Join(h, "leoflow-projects")
}

// restoreWorkspaceTree creates the workspace dir and writes every captured
// file. Intermediate directories are created as needed so a nested layout
// (subdir/dexaflow.yaml) reproduces correctly. Existing files are
// overwritten — the operator opted in to a restore.
func restoreWorkspaceTree(workspace string, files map[string][]byte) error {
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return fmt.Errorf("creating workspace dir: %w", err)
	}
	for rel, data := range files {
		dst := filepath.Join(workspace, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return fmt.Errorf("writing %s: %w", rel, err)
		}
	}
	return nil
}

// runPsqlRestore feeds the dump into the managed psql against the dev DB.
// The dump itself was produced with --clean --if-exists, so it idempotently
// drops + recreates objects; replaying it on top of an existing DB works as
// long as the schema is compatible (which the manifest guard already
// ensured).
func runPsqlRestore(ctx context.Context, leoflowHome string, dump []byte) error {
	psql := filepath.Join(leoflowHome, "postgres", "bin", "psql")
	if _, err := os.Stat(psql); err != nil {
		return fmt.Errorf("managed psql not found at %s — restore needs the managed PG path", psql)
	}
	dsn := devDSNs().database
	c := exec.CommandContext(ctx, psql, //nolint:gosec // managed binary + fixed args
		"--quiet", "--no-psqlrc", "--single-transaction",
		"--set=ON_ERROR_STOP=1",
		"--dbname="+dsn,
	)
	c.Stdin = strings.NewReader(string(dump))
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("psql restore failed: %w", err)
	}
	return nil
}
