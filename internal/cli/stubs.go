package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newServerCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "server",
		Short: "Information about running the control plane.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(),
				"The Dexaflow control plane runs as the 'dexaflow-server' binary, not via this CLI.\n"+
					"Run it directly (e.g. ./bin/dexaflow-server) with DEXAFLOW_* configuration.")
			return err
		},
	}
}
