package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// newForgetCommand removes a DAG (and, via ON DELETE CASCADE, its versions,
// runs, task instances, and XCom) from the Lite registry without touching the
// project files (issue #345). Complements the watcher's automatic set-diff for
// the case where the operator wants to deregister a DAG but keep the source
// directory — e.g. pause work on an example, scripted teardown.
//
// Implementation talks to the Lite DB directly (same pattern as
// reset-password). That works whether Lite is currently running or stopped,
// and the FK cascade in the schema handles versions/runs/TIs/XCom.
func newForgetCommand() *cobra.Command {
	var all, dryRun bool
	cmd := &cobra.Command{
		Use:   "forget [dag_id]",
		Short: "Remove a DAG (and all its history) from the Lite registry without touching the source files.",
		Long: "forget hard-deletes a DAG from the Lite registry. The dag.py and " +
			"dexaflow.yaml on disk are untouched — only the database rows go. " +
			"FK cascade handles versions, runs, task instances, and XCom. The " +
			"watcher will re-discover the project on the next tick if its files " +
			"are still present, so use this when you want to deregister AND " +
			"plan to delete the source files yourself, OR when you want a " +
			"clean re-registration after a manual database edit.\n\n" +
			"Run it as the same user as `leoflow lite` (no sudo). The Lite " +
			"Postgres must be reachable.",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) != 0 {
					return errors.New("--all takes no dag_id argument")
				}
				return nil
			}
			if len(args) != 1 {
				return errors.New("exactly one dag_id is required (or pass --all)")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			ids := args
			return runForget(cmd, ids, all, dryRun)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "deregister every DAG in the Lite registry")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print what would be deregistered without writing anything")
	return cmd
}

// runForget executes the deregister against the Lite DB. Returns nil on
// success; an error with operator-friendly framing on any failure. The DAG
// list is either the explicit ids argument or the full registry when --all.
func runForget(cmd *cobra.Command, ids []string, all, dryRun bool) error {
	ctx := cmdContext(cmd)
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: devDSNs().database})
	if err != nil {
		return fmt.Errorf("connecting to the Lite database (is Postgres up? start `leoflow lite`): %w", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	if all {
		dags, _, lerr := repo.ListDagsFiltered(ctx, "default", "", nil, 10_000, 0)
		if lerr != nil {
			return fmt.Errorf("listing dags: %w", lerr)
		}
		ids = make([]string, 0, len(dags))
		for _, d := range dags {
			ids = append(ids, d.DagID)
		}
	}

	if len(ids) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no DAGs to deregister") //nolint:errcheck // terminal output
		return nil
	}

	out := cmd.OutOrStdout()
	for _, id := range ids {
		if dryRun {
			_, _ = fmt.Fprintf(out, "  would forget %q\n", id) //nolint:errcheck // terminal output
			continue
		}
		if derr := repo.DeleteDag(ctx, "default", id); derr != nil {
			if errors.Is(derr, domain.ErrNotFound) {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  no DAG %q in registry\n", id) //nolint:errcheck // terminal output
				continue
			}
			return fmt.Errorf("deregistering %q: %w", id, derr)
		}
		_, _ = fmt.Fprintf(out, "  ✗ forgot %q (and all its versions, runs, task instances, XCom)\n", id) //nolint:errcheck // terminal output
	}
	return nil
}
