package cli

import (
	"context"
	"fmt"
	"io"
)

// checkDeployFollow validates --follow and --timeout before anything is
// built. Deploy registers only, so --follow needs --trigger; and one exit code
// reports one run, so --follow does not combine with --all.
func checkDeployFollow(o deployOptions) error {
	switch {
	case o.follow && !o.trigger:
		return fmt.Errorf("--follow needs --trigger: deploy only registers the DAG; add --trigger to start a run and follow it")
	case o.follow && o.all:
		return fmt.Errorf("--follow follows one run: deploy one DAG with --trigger --follow, or drop --follow with --all")
	}
	return checkFollowTimeout(o.follow, o.timeout)
}

// deployTrigger returns what deploy does once the DAG is registered (or found
// already current) when --trigger is set: start a run and, with --follow,
// follow it to its end. When the DAG is paused it says the run waits until the
// unpause. It returns nil without --trigger.
func deployTrigger(ctx context.Context, out io.Writer, serverURL, token string, o deployOptions) func(dagID string) error {
	if !o.trigger {
		return nil
	}
	return func(dagID string) error {
		runID, err := triggerDeployRun(ctx, out, serverURL, token, dagID)
		if err != nil {
			return err
		}
		if !o.follow {
			// With --follow the follower prints this note in its header.
			return notePausedRun(ctx, out, serverURL, token, dagID)
		}
		return followWithSignals(ctx, out, followTarget{base: serverURL, token: token, dagID: dagID, runID: runID},
			followOptions{timeout: o.timeout})
	}
}
