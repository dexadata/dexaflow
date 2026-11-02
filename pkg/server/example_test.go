package server_test

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dexadata/dexaflow/pkg/admission"
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

// A control plane that refuses every attempt of one tenant during a freeze,
// with an admission hook compiled into the binary instead of a webhook.
func ExampleWithAdmission() {
	freeze := admission.HookFunc(func(_ context.Context, req *admission.Request) (admission.Decision, error) {
		if req.TenantID == "acme" {
			return admission.Refuse("acme is frozen until 06:00 UTC"), nil
		}
		return admission.Admit(), nil
	})
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := server.Run(ctx, server.WithAdmission(freeze))
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(server.ExitCode(err))
	}
}
