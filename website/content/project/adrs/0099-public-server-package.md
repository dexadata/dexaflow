---
title: "ADR 0099: a public pkg/server builds the server, and components are replaced through options"
linkTitle: "0099 · Public server package"
weight: 990
description: "ADR 0099: the server's wiring moves out of cmd/dexaflow-server into internal/serverapp, a thin public pkg/server runs it, and the shipped binary is its first caller. Options replace the log sink, the XCom backend, the authenticator, the trusted issuers, the UI and, with ADR 0100, the admission hook. The contract types those options take move to public packages and the internal packages alias them. Everything under pkg/ follows one compatibility rule: no breaking change in a patch, a deprecation of one minor before a removal."
---

**Status:** Proposed
**Date:** 2026-10-29
**Relates:** ADR 0028 (one codebase, one license), ADR 0050 (MCP server), ADR 0068 (patch safety bar), ADR 0096 (task attempt usage events), ADR 0100 (admission hook)
**Issues:** #2176, #2178

## Context

Every server package lives under `internal/`, and the server is wired in
`cmd/dexaflow-server`: about 4,400 lines in 20 production files, and some 60
test files that test `package main`. An operator who wants the server with one
component replaced (a log sink for their storage, an authenticator for their
identity system, a policy on what a task may run) has to copy that wiring or
patch the source, and then carry the patch through every release.

The seams already exist and are injected: `logs.Sink`, `xcom.Backend`,
`auth.Authenticator`, `api.TrustedIssuer`, `api.UIServer`,
`executor.Executor`. None of them can be imported from another module,
because Go refuses an import of an `internal/` package from outside the module.

Two public packages already exist and took different routes. `pkg/client` is
generated and imports nothing internal. `pkg/dagspec` and `pkg/mcp` (#2178)
import `internal/` packages and re-export a few of their types as aliases. An
alias works for a caller, but the documentation of the type stays on the
internal package, which pkg.go.dev does not show, and any change to the
internal type changes the public one without anyone deciding it.

`examples/` is not a place for Go programs: it is embedded into every binary
(`//go:embed all:examples`) as the DAG samples the IDE materializes.

## Decision

1. **`pkg/server` runs the server.** It exposes
   `Run(ctx context.Context, opts ...Option) error`. Without options it
   reads the configuration the way the binary does (the config file, then
   the environment) and behaves exactly as `dexaflow-server` does today.
   `WithConfig` passes a configuration the caller already loaded.

2. **The wiring moves to `internal/serverapp`, and the binary calls
   `pkg/server`.** `cmd/dexaflow-server/main.go` keeps its flags and its
   exit codes and does nothing else. The move is its own change, with no
   edit beyond the package clause and the imports, so its review is a
   rename check and its tests move with it. The binary is the first caller
   of the public API, so an option that breaks is caught by the shipped
   build and its suites, not by an outside user.

3. **The first options are the seams that exist.**
   `WithLogSink`, `WithXComBackend`, `WithAuthenticator`,
   `WithTrustedIssuer` (repeatable), `WithUIServer`, and `WithAdmission` from
   ADR 0100. `WithUsageSink` waits for ADR 0096's events to merge, so the
   usage events change and this one do not edit the same wiring at once.
   An option replaces the component the configuration would have built;
   the configuration keys of the replaced component are ignored, and boot
   logs which component came from an option. The executor and the stores
   are not options: `executor.Request` changes in most patches and the
   stores are the schema, and freezing either would stop that.

4. **Contract types are defined in public packages.** The interfaces the
   options take, and every type their methods name (`logs.Ref`,
   `logs.LogWriter`, `xcom.Entry`, `auth.User`, `auth.Credentials`,
   `issuer.Identity`, and those of ADR 0100), move to `pkg/` packages
   named for them (`pkg/logsink`, `pkg/xcomstore`, `pkg/authn`,
   `pkg/admission`). The internal packages keep their names as aliases of
   the public ones, so no internal caller changes. Implementations stay
   internal. `pkg/mcp` and `pkg/dagspec` follow the same rule when one of
   their aliases next needs a change.

5. **One compatibility rule for `pkg/`.** A patch release never breaks an
   exported identifier of `pkg/`. A minor may, after the identifier was
   marked `Deprecated:` in the previous minor, and with a `Changed` or
   `Removed` changelog entry that says what to use instead. This mirrors
   how `/api/v2` is treated. Adding an option, a method to a struct, or a
   field to a struct is not a break; adding a method to an exported
   interface is, so an interface that may grow takes an optional second
   interface instead.

6. **Examples are `Example` functions.** A runnable program that builds a
   server with a custom log sink is an `Example` function of `pkg/server`:
   it compiles and runs with the package's tests and is shown on
   pkg.go.dev. `examples/` stays the DAG samples.

## Consequences

- An operator replaces a component with a small Go program that imports
  `pkg/server`, and upgrades by bumping the module version, with the
  compatibility rule telling them when to expect work.
- The move in step 2 touches every file of `cmd/dexaflow-server`, so it
  lands when no open change edits that directory: after ADR 0096's usage
  events stack (#2215, #2216, #2218, #2219), which edits `leader.go`.
- Each option is one more way to start the server; boot logging which
  components came from options keeps a support conversation short.
- The public contract types become reviewed API: a change to `logs.Ref`
  is now a change to `pkg/logsink.Ref`, and the compatibility rule applies.

## Alternatives considered

- **Alias internal types from `pkg/server`** (what `pkg/mcp` does). Smaller
  now, but the public documentation would be empty and an internal refactor
  would silently change the public API.
- **Plugins loaded at run time** (Go `plugin`, or a gRPC sidecar per seam).
  Go plugins need the exact toolchain and dependency versions of the host
  binary; sidecars add a network hop and a deployment to each seam. A small
  program built against `pkg/server` gives the same result with neither.
- **Leave the wiring in `cmd/` and export a `Build` function from there.**
  A `main` package cannot be imported.

## Plan

1. This ADR, accepted.
2. Move the wiring to `internal/serverapp` (rename only), after the usage
   events stack merges.
3. `pkg/server` with `Run`, `WithConfig` and the binary calling it.
4. The contract packages and the options, one PR per seam, each with its
   `Example` and the GoDoc of every export.
5. The compatibility rule in `AGENTS.md` and on the Go packages page.
