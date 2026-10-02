package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	leoflow "github.com/dexadata/dexaflow"
	"github.com/dexadata/dexaflow/internal/setup"
)

// pysrcMarker is the checksum sentinel written beside the extracted Python sources
// so a binary upgrade (new embedded parser vs stale on-disk copy) is detectable.
const pysrcMarker = ".leoflow-pysrc-checksum"

// pysrcRoot returns the extracted Python-sources root (~/.dexaflow/pysrc) that
// `leoflow setup` writes and the parser runs from.
func pysrcRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home for pysrc: %w", err)
	}
	return filepath.Join(stateDirIn(home), "pysrc"), nil
}

// parserPysrcDir returns the extracted parser-sources directory
// (~/.dexaflow/pysrc/parser) that ensurePysrc writes and the bundled
// `python3 -m leoflow_parser` imports from. It is empty when the home
// directory cannot be resolved — the caller then leaves PYTHONPATH untouched
// and falls back to the ambient environment.
func parserPysrcDir() string {
	root, err := pysrcRoot()
	if err != nil {
		return ""
	}
	return filepath.Join(root, "parser")
}

// withParserPythonPath returns env with the extracted parser-sources directory
// guaranteed on PYTHONPATH, preserving any entries already there (a caller's
// own PYTHONPATH, or CI's repo-root `parser`). On a binary-only install the
// default parser command is a bare `python3 -m leoflow_parser` and nothing
// else puts leoflow_parser on the import path, so the parser fails with
// ModuleNotFoundError (#587); this closes that gap without depending on the
// parser_cmd string carrying the path. The bundled parser is pure Python with
// vendored deps (ADR 0024), so this directory is all it needs to import.
// Idempotent: the directory is never added twice.
func withParserPythonPath(env []string) []string {
	dir := parserPysrcDir()
	if dir == "" {
		return env
	}
	const key = "PYTHONPATH="
	for i, e := range env {
		if !strings.HasPrefix(e, key) {
			continue
		}
		existing := e[len(key):]
		for _, p := range filepath.SplitList(existing) {
			if p == dir {
				return env // already present; nothing to do
			}
		}
		if existing == "" {
			env[i] = key + dir
		} else {
			env[i] = key + dir + string(os.PathListSeparator) + existing
		}
		return env
	}
	return append(env, key+dir)
}

// pythonSourcesChecksum hashes this binary's embedded parser+runtime sources, so a
// drifted on-disk extraction (the binary-upgrade case, #239) can be detected
// without re-extracting on every invocation.
func pythonSourcesChecksum() (string, error) {
	h := sha256.New()
	err := fs.WalkDir(leoflow.PythonSources(), ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := fs.ReadFile(leoflow.PythonSources(), p)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(h, "%s\x00", p) //nolint:errcheck // sha256 hash writer never errors
		h.Write(b)                  //nolint:errcheck // sha256 hash writer never errors
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hashing embedded python sources: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ensurePysrcIn re-extracts the bundled parser+runtime under dir when they are
// missing or have drifted from this binary's embedded copy — the binary-upgrade
// case (#239). Without it, `leoflow compile` runs against a stale parser after a
// manual binary swap (e.g. one predating dbt support), failing with a confusing
// "not supported by Leoflow" error instead of self-healing. The checksum guards
// against re-extracting on every call.
func ensurePysrcIn(dir string, logf func(format string, args ...any)) error {
	want, err := pythonSourcesChecksum()
	if err != nil {
		return err
	}
	marker := filepath.Join(dir, pysrcMarker)
	_, statErr := os.Stat(filepath.Join(dir, "parser", "leoflow_parser"))
	cur, _ := os.ReadFile(marker) //nolint:errcheck // an absent marker is a drift signal, not an error
	if statErr == nil && strings.TrimSpace(string(cur)) == want {
		return nil
	}
	if exErr := setup.ExtractFS(leoflow.PythonSources(), dir); exErr != nil {
		return fmt.Errorf("refreshing bundled parser sources under %s: %w", dir, exErr)
	}
	//nolint:errcheck // a failed marker write just re-extracts next time; not fatal
	_ = os.WriteFile(marker, []byte(want+"\n"), 0o600)
	if logf != nil {
		logf("↻ refreshed the bundled parser/runtime under %s (binary upgrade)", dir)
	}
	return nil
}

// ensurePysrc self-heals ~/.dexaflow/pysrc before compile runs the parser, so a
// binary upgrade never leaves `leoflow compile` on a stale parser. Best-effort: a
// failure is logged and compile proceeds with whatever is on disk (its own error
// surfaces if the parser truly cannot run), so this never blocks a working setup.
func ensurePysrc(cmd *cobra.Command) {
	dir, err := pysrcRoot()
	if err != nil {
		return
	}
	logf := func(format string, args ...any) { devPrintf(cmd.OutOrStdout(), format+"\n", args...) }
	if err := ensurePysrcIn(dir, logf); err != nil {
		logf("⚠ could not refresh bundled parser sources: %v (using what is on disk)", err)
	}
}
