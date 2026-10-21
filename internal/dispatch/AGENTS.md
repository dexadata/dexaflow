# internal/dispatch/

Rules for changing dispatch: resolving a task's execution context, minting the
agent's identity, and handing the request to the executor. The root
[AGENTS.md](../../AGENTS.md) applies too, and so do the review and performance
rules in [`internal/scheduler/AGENTS.md`](../scheduler/AGENTS.md): dispatch is
decision logic on the scheduler's hot path.

- **Every dispatch mints from the attempt epoch it claimed.** The agent token,
  the pod request and a warm assignment carry the epoch with its presence bit
  set (`dispatch_epoch_test.go`, ADR 0051 amendment). Storage matches every
  agent report and every settle on `(try_number, attempt_epoch)`, so a
  superseded attempt cannot write over its replacement
  (`internal/storage/attempt_fence_integration_test.go`,
  `attempt_settle_fence_integration_test.go`). A report that does not apply
  returns `ErrStaleReport`, which tells the agent to stop.
- **Tokens from before the epoch keep working.** A token without the claim is
  read as epoch 0 for reports and as any epoch of its try for heartbeats
  (`attemptEpochArg` in `internal/storage/agent_store.go`). A change to the
  fence keeps both readings, or an upgrade kills live attempts.
- **Warm placement degrades, it never strands.** A warm worker exists before
  any task is known, so a task that declares its own placement or pod metadata
  (node selector, tolerations, runtime class and the rest listed in
  `internal/domain/warm_placement_test.go`) takes the dedicated pod path
  instead. The rule lives in `domain.WarmPolicy.DedicatedReason`, the one
  decision `Dispatch`, the register response (#1707) and the warm targets
  (`HasWarmTask`, its per-task half, in `ActiveWarmTargets`, #2065) use:
  change it there, never with a check of its own in dispatch, and keep
  `Dispatcher.WarmPolicy` and the server's `warmPolicy(cfg)` equal
  (`warm_policy_test.go` here and in `cmd/dexaflow-server`).
- **A refusal is permanent.** A task that may not run here (it does not fit
  the size ADR 0066 charges it) fails on its first attempt with the reason,
  and spends neither dispatch retries nor its own retries
  (`internal/scheduler/dispatch_refused_test.go`).
