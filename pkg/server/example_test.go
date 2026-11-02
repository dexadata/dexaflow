package server_test

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dexadata/dexaflow/pkg/server"
)

// A program that runs the control plane as dexaflow-server does: configured
// by DEXAFLOW_CONFIG and the DEXAFLOW_* environment, stopped by SIGINT or
// SIGTERM. It needs a database, so it has no Output line.
func ExampleRun() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
