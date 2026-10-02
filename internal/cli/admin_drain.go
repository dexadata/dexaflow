package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// drainDefaultTimeout bounds how long drain waits for active runs to finish
// before reporting what is still running and exiting non-zero.
const drainDefaultTimeout = 10 * time.Minute

// drainDefaultPollInterval is how often drain re-checks for active runs.
const drainDefaultPollInterval = 5 * time.Second

// drainOptions holds the resolved flags for a drain run.
type drainOptions struct {
	timeout      time.Duration
	pollInterval time.Duration
	wait         bool
}

// newAdminDrainCommand builds `dexaflow admin drain`: safely quiesce the control
// plane before maintenance or an upgrade. It pauses every DAG (no new runs),
// then polls active runs until none remain or --timeout elapses; on timeout it
// reports what is still running and exits non-zero.
func newAdminDrainCommand() *cobra.Command {
	var f adminFlags
	var o drainOptions
	var noWait bool
	cmd := &cobra.Command{
		Use:   "drain",
		Short: "Pause every DAG, then wait for active runs to finish (quiesce for maintenance).",
		Long: "Safely quiesce the control plane before maintenance or an upgrade: pause " +
			"every registered DAG so no new runs start, then poll active runs until none " +
			"remain or --timeout elapses. On timeout it prints the still-running runs and " +
			"exits non-zero. Use --no-wait to pause without waiting.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if noWait {
				o.wait = false
			}
			c, base, err := adminClient(cmd, f)
			if err != nil {
				return err
			}
			return runDrain(cmdContext(cmd), cmd.OutOrStdout(), c, base, o)
		},
	}
	addAdminFlags(cmd, &f)
	cmd.Flags().DurationVar(&o.timeout, "timeout", drainDefaultTimeout, "max time to wait for active runs to drain")
	cmd.Flags().DurationVar(&o.pollInterval, "poll-interval", drainDefaultPollInterval, "how often to re-check for active runs")
	cmd.Flags().BoolVar(&o.wait, "wait", true, "poll active runs until they drain")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "pause every DAG but do not wait for active runs")
	return cmd
}

// runDrain executes the drain sequence: announce the plan, pause every DAG,
// then (unless --no-wait) poll until active runs clear or the timeout fires.
func runDrain(ctx context.Context, w io.Writer, c *apiclient.ClientWithResponses, base string, o drainOptions) error {
	ids, err := listAllDagIDs(ctx, c)
	if err != nil {
		return err
	}
	if perr := renderDrainPlan(w, base, ids, o); perr != nil {
		return perr
	}
	if serr := setPausedForDAGs(ctx, w, c, ids, true); serr != nil {
		return serr
	}
	if !o.wait {
		return writeLine(w, "Paused every DAG; not waiting for active runs (--no-wait).")
	}
	remaining, timedOut, werr := waitForRunsToDrain(ctx, c, o.timeout, o.pollInterval)
	if werr != nil {
		return werr
	}
	if timedOut {
		if rerr := renderRuns(w, remaining); rerr != nil {
			return rerr
		}
		return fmt.Errorf("drain timed out after %s: %d active run(s) remain", o.timeout, len(remaining))
	}
	return writeLine(w, "Drain complete: no active runs remain.")
}

// renderDrainPlan prints a summary of what drain is about to pause and how long
// it will wait — the "print a summary of what it will pause" contract.
func renderDrainPlan(w io.Writer, base string, ids []string, o drainOptions) error {
	lines := []string{fmt.Sprintf("Draining %s", base)}
	if len(ids) == 0 {
		lines = append(lines, "  no registered DAGs to pause")
	} else {
		lines = append(lines, fmt.Sprintf("  pausing %d DAG(s): %s", len(ids), strings.Join(ids, ", ")))
	}
	if o.wait {
		lines = append(lines, fmt.Sprintf("  then waiting up to %s for active runs to finish", o.timeout))
	} else {
		lines = append(lines, "  not waiting for active runs (--no-wait)")
	}
	return writeLine(w, strings.Join(lines, "\n"))
}

// waitForRunsToDrain polls for running runs across every DAG until none remain
// or the timeout elapses. It returns the runs still active (empty when drained),
// whether it timed out, and any hard error. A three-value return keeps the
// drained case explicit (rather than an ambiguous nil, nil).
func waitForRunsToDrain(ctx context.Context, c *apiclient.ClientWithResponses, timeout, pollInterval time.Duration) (remaining []adminRun, timedOut bool, err error) {
	deadline := time.Now().Add(timeout)
	for {
		running, cerr := collectRuns(ctx, c, "", string(apiclient.ListDagRunsParamsStateRunning))
		if cerr != nil {
			return nil, false, cerr
		}
		if len(running) == 0 {
			return nil, false, nil
		}
		if !time.Now().Before(deadline) {
			return running, true, nil
		}
		select {
		case <-ctx.Done():
			return running, false, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}
