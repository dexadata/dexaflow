package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/version"
	"github.com/dexadata/dexaflow/migrations"
)

// newBackupCommand wires `dexaflow lite backup`. The whole Lite install ships
// inside one tarball: workspace DAGs, a logical pg_dump of the managed
// datastore, the config.yaml (admin hash, JWT secret, and the key that decrypts your connection secrets), and a small
// MANIFEST.json that lets restore decide whether the bundle is compatible
// (#137).
func newBackupCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Snapshot the Lite install (workspace + datastore + config) into a portable archive.",
		Long: "backup writes a tar.gz containing your workspace DAGs, a logical pg_dump " +
			"of the managed Postgres, the config (admin hash, JWT secret, and the key that decrypts your connection secrets), and a small " +
			"MANIFEST.json. Pair with `dexaflow lite restore` to migrate to another machine, " +
			"survive an OS reinstall, or roll back a botched pre-alpha upgrade.\n\n" +
			"Backup only covers the managed Postgres path (the default Lite shape). " +
			"For the Docker datastore path, capture the volume with `docker volume export` " +
			"instead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBackup(cmd, output)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "",
		"path for the archive (default: leoflow-backup-<timestamp>.tar.gz in the cwd)")
	return cmd
}

func runBackup(cmd *cobra.Command, output string) error {
	out := cmd.OutOrStdout()
	home := invokingUserHome()
	if home == "" {
		return fmt.Errorf("could not resolve the user home directory")
	}
	leoflowHome := stateDirIn(home)
	if _, err := os.Stat(leoflowHome); err != nil {
		return fmt.Errorf("no Lite install found at %s — run `dexaflow setup` first", leoflowHome)
	}
	cfg := loadUserConfig(home)
	if cfg == nil {
		return fmt.Errorf("could not read %s/config.yaml", leoflowHome)
	}
	if output == "" {
		output = defaultBackupOutputPath(time.Now())
	}

	schema, err := migrations.Latest()
	if err != nil {
		return fmt.Errorf("reading embedded schema version: %w", err)
	}
	pgVersion := detectPostgresVersion(cmd.Context(), leoflowHome)
	manifest := newBackupManifest(version.Get().Version, schema, pgVersion)
	manifestBytes, err := marshalManifest(manifest)
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}

	devPrintf(out, "▸ snapshotting datastore via pg_dump …\n")
	dumpPath := filepath.Join(os.TempDir(), fmt.Sprintf("leoflow-backup-dump-%d.sql", os.Getpid()))
	defer func() { _ = os.Remove(dumpPath) }() //nolint:errcheck // temp file cleanup; the dump path is ours
	if derr := runPgDump(cmd, leoflowHome, dumpPath); derr != nil {
		return derr
	}

	devPrintf(out, "▸ bundling archive %s …\n", output)
	if err := writeBackupArchive(output, leoflowHome, cfg.Workspace, dumpPath, manifestBytes); err != nil {
		return fmt.Errorf("writing archive: %w", err)
	}
	stat, serr := os.Stat(output)
	if serr != nil {
		devPrintf(out, "✓ backup written: %s\n", output)
	} else {
		devPrintf(out, "✓ backup written: %s (%d bytes)\n", output, stat.Size())
	}
	devPrintln(out, "  manifest: "+strings.ReplaceAll(string(manifestBytes), "\n", "\n  "))
	return nil
}

// defaultBackupOutputPath builds the default --output path for `dexaflow lite
// backup` — `leoflow-backup-<UTC timestamp>.tar.gz` in the current directory.
// Extracted from runBackup so the format is unit-testable (operators script
// against it; a regression to local time or a different separator would
// silently break automations).
func defaultBackupOutputPath(now time.Time) string {
	return filepath.Join(".", "leoflow-backup-"+now.UTC().Format("2006-01-02T150405Z")+".tar.gz")
}

// detectPostgresVersion reads the managed PG version string via `postgres
// --version`. Empty on failure so the manifest just omits it; the version
// is informational, not a restore gate (the gate is the schema version).
func detectPostgresVersion(ctx context.Context, leoflowHome string) string {
	pg := filepath.Join(leoflowHome, "postgres", "bin", "postgres")
	cmd := exec.CommandContext(ctx, pg, "--version") //nolint:gosec // managed binary, fixed argument
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// runPgDump invokes the managed pg_dump against the dev DB and writes a
// plain SQL file (logical, format=plain) so restore can replay it with psql
// against any compatible Postgres minor.
func runPgDump(cmd *cobra.Command, leoflowHome, dumpPath string) error {
	pgDump := filepath.Join(leoflowHome, "postgres", "bin", "pg_dump")
	if _, err := os.Stat(pgDump); err != nil {
		return fmt.Errorf("managed pg_dump not found at %s — is this a Docker-datastore install? "+
			"backup currently supports the managed PG path only (see docs)", pgDump)
	}
	dsn := devDSNs().database // unix socket, trust auth — same path the server uses
	args := []string{
		"--clean", "--if-exists", // make the dump idempotent so restore is repeatable
		"--no-owner", "--no-privileges", // strip env-specific role grants
		"--format=plain",
		"--dbname=" + dsn,
		"--file=" + dumpPath,
	}
	dump := exec.CommandContext(cmd.Context(), pgDump, args...) //nolint:gosec // managed binary + fixed args; dsn comes from devDSNs
	dump.Stderr = cmd.ErrOrStderr()
	if err := dump.Run(); err != nil {
		return fmt.Errorf("pg_dump failed: %w (is `dexaflow lite` running?)", err)
	}
	return nil
}

// writeBackupArchive assembles the tar.gz a backup ships in. Layout, driven
// by TestArchiveRoundTrip:
//
//	MANIFEST.json
//	config.yaml          (if present in leoflowHome)
//	setup.json           (if present in leoflowHome)
//	datastore.sql        (the pg_dump produced separately)
//	workspace/<rel>      (every regular file under workspace, .git etc. skipped)
//
// Empty/missing leoflowHome files are tolerated so a partial install can
// still be snapshotted; restore knows how to tolerate a partial bundle on
// the read side.
func writeBackupArchive(output, leoflowHome, workspace, dumpPath string, manifest []byte) error {
	f, err := os.Create(output) //nolint:gosec // user-chosen output path
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }() //nolint:errcheck // best-effort close after write
	gz := gzip.NewWriter(f)
	defer func() { _ = gz.Close() }() //nolint:errcheck // gzip footer flushed on close
	tw := tar.NewWriter(gz)
	defer func() { _ = tw.Close() }() //nolint:errcheck // tar footer flushed on close

	if err := writeTarFile(tw, "MANIFEST.json", manifest); err != nil {
		return err
	}
	for _, p := range []string{"config.yaml", "setup.json"} {
		full := filepath.Join(leoflowHome, p)
		data, rerr := os.ReadFile(full) //nolint:gosec // managed home; path is fixed
		if rerr != nil {
			continue
		}
		if werr := writeTarFile(tw, p, data); werr != nil {
			return werr
		}
	}
	if dumpPath != "" {
		dump, rerr := os.ReadFile(dumpPath) //nolint:gosec // temp file we just wrote
		if rerr != nil {
			return fmt.Errorf("reading pg_dump output: %w", rerr)
		}
		if werr := writeTarFile(tw, "datastore.sql", dump); werr != nil {
			return werr
		}
	}
	if workspace == "" {
		return nil
	}
	return writeWorkspaceTree(tw, workspace)
}

// writeTarFile emits one in-memory blob as a regular tar entry.
func writeTarFile(tw *tar.Writer, name string, data []byte) error {
	if err := tw.WriteHeader(&tar.Header{
		Name: name, Mode: 0o600, Size: int64(len(data)),
		ModTime: time.Now().UTC(), Typeflag: tar.TypeReg,
	}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// writeWorkspaceTree walks the workspace and adds each regular file under
// workspace/<rel> inside the archive. Excluded dirs (see isWorkspaceSkipDir)
// are pruned via filepath.SkipDir so .git history and venv cruft never land
// in a backup.
func writeWorkspaceTree(tw *tar.Writer, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		base := filepath.Base(rel)
		if info.IsDir() && isWorkspaceSkipDir(base) {
			return filepath.SkipDir
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // walking the user's workspace by their own request
		if rerr != nil {
			return rerr
		}
		return writeTarFile(tw, filepath.Join("workspace", rel), data)
	})
}

// isWorkspaceSkipDir reports whether a workspace subdirectory should be
// excluded from the backup as noise. Same list as `tar --exclude-vcs` plus a
// few Python/JS regulars.
func isWorkspaceSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", ".venv", "venv", "__pycache__",
		".pytest_cache", "node_modules", ".tox", ".mypy_cache":
		return true
	}
	return false
}
