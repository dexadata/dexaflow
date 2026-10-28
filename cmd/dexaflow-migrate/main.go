// Command dexaflow-migrate applies the Dexaflow schema migrations to Postgres.
// It is the entrypoint of the dexaflow-migrate image, which the Helm chart's
// pre-install and pre-upgrade hook Job runs (`-path=/migrations
// -database=<url> up`), and it takes the golang-migrate CLI's subcommands
// (`up`, `goto`, `force`, `version`) so the recovery steps of the upgrade
// guide work unchanged. Errors name the cause and the chart value to change,
// and the exit code tells a deterministic failure from a transient one; see
// package internal/migrator.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/dexadata/dexaflow/internal/migrator"
	"github.com/dexadata/dexaflow/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := migrator.Main(ctx, os.Args[1:], os.Stdout, os.Stderr, versionString())
	stop()
	os.Exit(code)
}

// versionString is what -version prints: this build and the golang-migrate
// version compiled into it.
func versionString() string {
	v := version.Get()
	gm := "unknown"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/golang-migrate/migrate/v4" {
				gm = d.Version
			}
		}
	}
	return fmt.Sprintf("dexaflow-migrate %s (commit %s, golang-migrate %s, %s)", v.Version, v.GitCommit, gm, v.GoVersion)
}
