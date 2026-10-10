package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// loadDbtManifest returns the dbt manifest.json bytes. A pinned dbt.manifest is used
// as-is; otherwise it runs a fresh `dbt parse` so a model edit reflects on hot-reload
// — and in Lite (local) it parses with the per-DAG venv's dbt rather than a system one
// the user may not have (L1). The manifest is resolved relative to the dbt project.
//
// The fresh-parse path shells out to the dbt binary — an external-process
// orchestration only honestly exercised end-to-end (the e2e-dbt job), so this file is
// excluded from the unit-coverage floor (ADR 0011), like the other external-binary
// orchestrators (managed_postgres.go, …). The pure branches (a pinned manifest, the
// dbt-not-found error, the venv/profile resolution) are unit-tested via their helpers.
// localWarehouse is NOT "where the DAG will run" in general — the dbt binary
// above is chosen without reference to it (#993). It says specifically that this
// compile targets Lite's zero-config duckdb warehouse, which genuinely is a
// property of the runtime target: that warehouse exists only for the subprocess
// executor, and `dbt parse` needs the same profile the task will get. Writing a
// duckdb stub for an image-bound compile would let a DAG destined for Snowflake
// parse against duckdb and produce a manifest for the wrong adapter — a green
// compile with a silently wrong artifact, which is the class this file has spent
// the release removing.
func loadDbtManifest(cmd *cobra.Command, dir string, c *domain.DbtConfig, localWarehouse bool, dagID string) ([]byte, error) {
	projectDir := filepath.Join(dir, c.Project)
	if c.Manifest != "" {
		path := filepath.Join(projectDir, c.Manifest)
		data, rerr := os.ReadFile(path) //nolint:gosec // G304: operator-supplied project path.
		if rerr != nil {
			return nil, fmt.Errorf("reading dbt manifest %s: %w", path, rerr)
		}
		return data, nil
	}
	dbtBin := dbtParseBin(dagID)
	if dbtBin == "dbt" {
		if _, lerr := exec.LookPath("dbt"); lerr != nil {
			return nil, fmt.Errorf("dbt is not on PATH: install dbt-core and your adapter (e.g. `pip install dbt-postgres`), or set dbt.manifest in dexaflow.yaml to a pre-built manifest.json")
		}
	}
	pc := exec.CommandContext(cmdContext(cmd), dbtBin, "parse") //nolint:gosec // dbtBin is the resolved venv or PATH dbt
	pc.Dir = projectDir
	pc.Env = os.Environ()
	// Zero-config local warehouse: `dbt parse` needs a profile too, so give it a
	// default duckdb one when the Lite project has no connection and no profiles.yml
	// — the compile-time half of L4 (the runtime writes the same at task time).
	if localWarehouse && c.Connection == "" {
		if pdir := writeParseDuckdbProfile(dir, c); pdir != "" {
			defer func() { _ = os.RemoveAll(pdir) }() //nolint:errcheck // best-effort cleanup of a temp dir
			pc.Env = append(pc.Env, "DBT_PROFILES_DIR="+pdir)
		}
	}
	// dbt logs a parse failure's cause (a missing `dbt deps`, a bad profile) to
	// STDOUT, not stderr — so a bare exec error surfaces as "exit status 2" with no
	// reason, blinding CI and forcing a manual repro. Tee stderr to the CLI as
	// before and capture BOTH streams; on failure, attach the tail so the dbt
	// diagnostic travels with the returned error.
	var captured bytes.Buffer
	pc.Stdout = &captured
	pc.Stderr = io.MultiWriter(cmd.ErrOrStderr(), &captured)
	if rerr := pc.Run(); rerr != nil {
		if tail := lastLines(captured.String(), 20); tail != "" {
			return nil, fmt.Errorf("dbt parse in %s: %w\n%s", projectDir, rerr, tail)
		}
		return nil, fmt.Errorf("dbt parse in %s: %w", projectDir, rerr)
	}
	path := filepath.Join(projectDir, "target", "manifest.json")
	data, rerr := os.ReadFile(path) //nolint:gosec // derived from operator-supplied project path.
	if rerr != nil {
		return nil, fmt.Errorf("reading dbt manifest %s: %w", path, rerr)
	}
	return data, nil
}
