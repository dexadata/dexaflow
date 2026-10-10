# Cloud experiments

Three things a local k3d cannot answer, each a bounded window with its own
teardown. The weekend soak stays local and free; this directory exists only for
what genuinely needs a real cluster.

## Why these three, and only these three

| Experiment | Why local cannot answer it |
|---|---|
| `pod-per-task` | The soak runs `dexaflow lite --executor subprocess`. The **entire Kubernetes executor path** is therefore unmeasured: pod-per-task, image pulls, the pod informer, the reaper acting on pods. That is the path production runs. |
| `warm-pool-ab` | Warm pools exist only on Kubernetes (`internal/executor`), and Lite's subprocess executor has no pool at all. |
| `netpol` | k3d **accepts NetworkPolicy and ignores it**, so #1089's standing rows pass vacuously. Dataplane V2 enforces, which is the whole point. |

## Budget

Estimated at us-central1 list prices, on-demand, printed by `provision.sh`
before anything is created. Confirm against the pricing calculator; these are
estimates the script uses so an operator sees a figure before approving.

| Experiment | Shape | Window | Estimate |
|---|---|---|---|
| `pod-per-task` | 10 x `e2-standard-4` | 6h | ~USD 8.64 |
| `warm-pool-ab` | 3 x `e2-standard-4` | 6h | ~USD 3.01 |
| `netpol` | 3 x `e2-standard-4` | 2h | ~USD 1.00 |
| **all three, once** | | | **~USD 13** |

Every figure is what `provision.sh --dry-run` prints for that shape, so the table
and the script cannot drift apart. The estimate covers **nodes and the cluster
fee and nothing else**: it excludes boot disks (100 GB of pd-balanced per node by
default, about USD 0.014 an hour each, which is roughly a tenth of the node cost
at ten nodes), egress, load balancers, and anything left behind after the cluster
is deleted. It also ignores any GKE free-tier credit, so it errs high on the
cluster fee and low on everything it omits.

The authorized ceiling is USD 50, which buys roughly three full passes. **The
extra headroom is not a reason to spend it.** More nodes do not make these
answers truer: the saturation experiment is the only one whose result depends on
node count, and the other two would just idle hardware. What the headroom really
buys is **re-runs**, and re-runs are worth paying for, because one run is one
sample and a single number from a shared cloud is not a measurement.

### The number that actually matters

| | |
|---|---|
| A four-hour window, 3 nodes | ~USD 2.01 |
| The same cluster left up for a month | ~USD 361.44 |
| Ten nodes left up for a month | ~USD 1036.80 |

Both of the bottom rows come from `forgotten_usd`, which is `estimate_usd` over
720 hours, so they are the script's own arithmetic rather than a remembered
number. (An earlier draft of this table said USD 220 for the middle row, which
was nothing the script would print.)

Two orders of magnitude. Every guardrail here aims at the second number.

## Guardrails

**`--max-run-duration` on the node pool is the one that counts.** The nodes carry
their own expiry and Google enforces it. If the script dies, if the laptop
sleeps, if the teardown never runs, the expensive part still goes away. Nothing
else in this directory has that property, because everything else depends on
something of ours continuing to run.

Two things about it are worth stating precisely, because the first draft of this
directory got both wrong:

- **It is a NODE-POOL flag.** `gcloud container clusters create` does not accept
  `--max-run-duration`; it exists on `container node-pools create|update`. So
  `provision.sh` creates the cluster, puts the TTL on `default-pool` in a second
  call, **reads it back**, and deletes the cluster immediately if it is not
  there. A cluster whose nodes have no expiry is the one outcome this directory
  exists to prevent, so it is not left standing on a hope.
- **What expiry means is now VERIFIED on a live cluster, and it is the good
  outcome.** The API says `NodeConfig.maxRunDuration` is "the maximum duration
  for the nodes to exist. If unspecified, the nodes can exist indefinitely."
  What it does not say is whether GKE then replaces an expired node to hold the
  pool at its target count. It does not.

  Observed 2026-09-20 on `leoflow-exp-netpol-09192248` (1 node, `--ttl 30m`,
  created `01:48:43Z`), inspected an hour later at `02:48Z`:

  | | |
  |---|---|
  | node pool `default-pool` | `status: RUNNING`, `initialNodeCount: 1`, `maxRunDuration: 1800s` |
  | its managed instance group | **`size 0`, `targetSize 0`** |
  | GCE instances in the project | **none** |
  | the cluster object | still `RUNNING` |

  So the expensive part really does remove itself and is **not** recreated: the
  MIG target is taken to zero rather than the node being replaced. That is the
  guardrail working exactly as this directory needs it to.

  **And it confirms the other half.** The cluster was still `RUNNING` an hour
  later with zero nodes, billing the control-plane fee (~USD 0.10/h) the whole
  time. The TTL bounds the node bill; only deleting the CLUSTER stops the rest.
  The TTL is a backstop, the teardown is still the job, and `--list` is still
  the check.

- **Applying it takes real time, and that time is a gap in the guardrail.**
  `maxRunDuration` recreates every node one at a time. At ten nodes that ran over
  thirty minutes and outran gcloud's own operation wait, which returned non-zero
  with `NODES_FAILED: 0` at 9 of 10 nodes while the operation went on to finish
  `DONE`. Until that roll completes there is a cluster with no expiry on it,
  which is the state everything here exists to avoid. #1206 stops the script
  mistaking a gcloud timeout for a failure; #1207 proposes applying the TTL to a
  one-node pool and resizing afterwards, so the roll is one node instead of ten.

On top of it:

- **A hard refusal above 10 nodes**, because the failure mode of a fat-fingered
  `--nodes` is a bill and not an error.
- **A hard refusal above a 24h TTL.** These are bounded experiments.
- **No autoscaling.** A fixed pool cannot grow into a bill.
- **Zonal, never regional**, because a regional control plane multiplies nodes
  across zones.
- **An unpriced machine type is refused** rather than created with no estimate.
- **The teardown verifies.** It re-describes the cluster after deleting and
  fails loudly if it is still there, because a teardown that trusts its own exit
  code is how a cluster outlives the script that claimed to remove it.
- **The teardown only deletes what this repo labelled**, matching the whole
  `purpose=leoflow-experiment` pair and not a substring. The first version
  matched `purpose=leoflow-experiment-anything` and would have deleted a cluster
  it did not create; its own self-test caught that.

- **A TTL that is not a positive number is refused**, not passed on. Only the
  upper bound used to be checked, so `--ttl -2h` and `--ttl abch` became a
  negative and a zero duration handed to Google.

Nothing about the account is in this repository. Project and zone come from the
environment or `gcloud config`, and every script refuses to guess. The cluster
name `provision.sh` leaves for `teardown.sh` is gitignored: it names a real,
billing cluster and belongs to one run on one machine.

## What a cluster delete does NOT remove

Deleting the cluster is not the end of the bill. Four things live outside it and
survive it:

| Survivor | How it appears | What it costs |
|---|---|---|
| Persistent disks | a PVC that outlived its cluster, which is exactly the shape of leoflow's per-run staging volumes (ADR 0022) | pd-balanced is about USD 0.10 per GB per month, billed whether or not anything is attached |
| Forwarding rules and target pools | a `Service` of type `LoadBalancer` in an experiment | a few USD a month each, plus the address |
| Reserved static addresses | left by the above | billed **while reserved and unused** |
| Artifact Registry repositories | the runners do not exist yet, and the first one will push DAG images somewhere the cluster can pull from | storage per GB per month, indefinitely, and nothing here ever deletes it |

`teardown.sh --leftovers` lists all four. It deletes nothing: none of them
carries the `purpose=leoflow-experiment` label that makes deleting a cluster
safe, so there is no way for a script to tell ours from the project's own, and a
teardown that guesses about that is worse than one that prints a list.

## Spot is deliberately off

Spot nodes are 60% or more cheaper and are the wrong choice here. **A preemption
looks exactly like the infrastructure failure these experiments measure**, so on
a resilience run it is a confound rather than a saving. `--spot` exists for
smoke-testing the scripts themselves and prints a warning saying so.

It also saves less than it looks on a short window: the discount applies to the
nodes, not to the control-plane fee, which dominates a few hours at small node
counts.

## Running one

```bash
export GCP_PROJECT=...        # never committed
export GCP_ZONE=us-central1-a

test/gcp/provision.sh --dry-run --experiment netpol     # plan and cost, creates nothing
test/gcp/provision.sh --experiment netpol --ttl 2h
# ... run the experiment ...
test/gcp/teardown.sh                                    # deletes and verifies
test/gcp/teardown.sh --list                             # what is still billing
test/gcp/teardown.sh --leftovers                        # disks/LBs/registries a delete leaves
```

`teardown.sh --all` removes every cluster this repo labelled, and is the thing
to run when you are not sure. It selects them with a gcloud `--filter`, which is
documented as a word match rather than an exact one for some APIs, so anything
it selects is checked again against the whole `purpose=leoflow-experiment` pair
before it is deleted. A cluster that fails that check is skipped and named, not
deleted and not fatal, so one lookalike cannot abort the sweep.

## The runners

Three, one per experiment, plus the harness they share. Every one of them
provisions through `provision.sh` and tears down through `teardown.sh`, and
neither of those is ever bypassed: the guardrails, the cost plan and the node
TTL all live there, and a second path to creating a cluster would be a second
path with none of them.

| File | What it does | Has it run? |
|---|---|---|
| `netpol.sh` | #1089's §3b rows on a CNI that enforces | **yes**, see below |
| `pod-per-task.sh` | rising concurrency until something saturates | **yes**, the Kubernetes half; see the run record |
| `warm-pool-ab.sh` | warm pools vs the coupled baseline | **no**, and two known defects say not yet (#1203) |
| `lib/experiment.sh` | the run directory and the teardown trap | |
| `lib/stats.sh` | percentiles and what "saturated" means | |
| `lib/stack.sh` | Postgres, Redis and `helm install` on the cluster | |

Each is `--self-test`able with no cloud, no cluster and no credentials, and
`scripts/check-script-selftests.sh` finds them by the presence of a
`self_test()` and runs them in CI. Run one with `--execute`; without it you get
the cost plan and nothing is created.

### The teardown is a trap, not a final line

`lib/experiment.sh` installs the teardown on `EXIT`, `INT` and `TERM` *before*
the cluster is created, and the trap is the only thing that deletes. A runner
that reached its last line and called `teardown.sh` there would have one exit
path that works and several that do not: a failed assertion, a `set -e` trip in
a helper, an unbound variable, a Ctrl-C. All of those have now happened during
development, and the cluster went away every time. That is checked by a
self-test that drives a stub teardown through four shapes, including the one
that matters most: **a teardown that itself fails turns a passing run red** and
prints the manual delete command, because a teardown failure reported quietly is
the only outcome the budget cannot absorb.

### What "saturated" means was decided before the first run

In `lib/stats.sh`, in code, self-tested: a level is saturated when its p95 is at
least **3x** the p95 of the lowest level **and** the level before it was already
at **2x**. The second clause is the whole point. One level out of line on shared
cloud hardware is a neighbour's job, not a wall, and it is reported as a `spike`
rather than a finding. A level with fewer than **20** observations is
`inconclusive` whatever its p95 says.

The answer is the phase whose wall appears at the lowest concurrency, and **a
tie is reported as a tie**: "the API server and image pulls went together" is a
real answer, and picking one of them would send an operator to fix the wrong
thing with full confidence. No saturation at all is `no-saturation-observed`,
which is *not* "it scales" and says so in those words.

## What has actually been run

**`netpol.sh`, on GKE Dataplane V2.** See the run record below. It needs no
leoflow control plane at all: the chart's task NetworkPolicy selects pods by
`leoflow.io/run-id`, so a probe pod wearing that label is subject to exactly the
policy a task pod would be. That is a deliberate narrowing. It proves the
**policy and the CNI**; it does not prove that the executor puts that label on a
real task pod, which is a separate claim about `executor/kubernetes.go`.

**`pod-per-task.sh --k8s-only`** drives pods directly, with no leoflow in the
loop. It can rank three of the four candidate ceilings (API server, image pull,
node capacity) and **cannot see the fourth**, the scheduler tick, which is the
one that needs a control plane. It reports that phase as absent rather than as
zero.

**`warm-pool-ab.sh` has never run end to end**, and prints the banner saying so.

This paragraph has now been wrong in both directions, which is worth recording
next to what it currently says. It first said the runner "refuses to provision",
and that was TRUE: an unconditional refusal sat after the `--execute` check, so
the flag did nothing. I then "corrected" it to say the refusal did not exist,
having read the `--execute` branch and not the line below it, and that correction
shipped. The refusal is now removed, so the sentence is true for the first time,
and it is true because the code changed rather than because the description got
better.

The refusal's own message is the reason it went: it said the DAG build-and-push
step was not implemented, which was true when written and false by the time it
was read. A guard whose stated reason has stopped being true is worse than no
guard, because it is specific, so it is believed.

The step it was missing is the same one `test/soak/warmpool-ab.sh` was missing:
building and pushing the DAG image, without which every `trigger_and_wait` 404s
against a dag_id the control plane was never told about. **That step is now
written and has been run for real.** `wp_build_and_push` substitutes the project
id into `test/gcp/dags/gcp_probe/leoflow.yaml` (which carries `${GCP_PROJECT}` in
git so no account identifier is committed) and shells out to the CLI's own
compile, which cross-builds `linux/amd64` from an arm64 Mac and pushes to
Artifact Registry. The pushed image was deleted again afterwards, because a
registry repository outlives every cluster that pulled from it and carries none
of the labels that make deleting a cluster safe.

**What is still unproven is everything from the control-plane login onward**: the
token, the deploy, the three arms, the drift check between the two arm-A
measurements. That code is a specification that compiles.

**And two defects in it are already known, so do not run it yet** (#1203). Reading
it against `internal/executor/warmpool.go` found both:

- `wp_run_arm` measures each attempt from the POD (`PodScheduled` to
  `state.running.startedAt`). That interval exists per attempt in arms A and A',
  where dispatch is pod-per-task, and **does not exist per attempt in arm B**: a
  warm attempt is pushed to a worker the reconciler created earlier to hold
  `EffectiveMinIdle`, so those pods carry the pool fill's startup, sampled once
  per worker. The two arms would be timing different events, and the resulting
  speedup would look like a result. The interval defined identically under both
  arms is the control plane's own, `queued_at` to `start_date` on the task
  instance.
- `wp_report` is unimplemented and returns non-zero, so a run ends with raw pod
  JSON and no summary.

The rest of the defect class that has already bitten this directory four times (a
flag on the wrong subcommand, a label split on the wrong separator, a missing
release channel, an empty-array expansion under bash 3.2) is the kind only a real
run finds. These two were cheaper: they were found by reading, before a cluster
was paid for.

### Run record: `pod-per-task`, 2026-09-20, 10 x e2-standard-4

Cluster `leoflow-exp-pod-per-task-09201532`, `us-central1-a`, `--ttl 3h`,
`--k8s-only`, ladder 10/20/40/80/160/320, `LOAD_SLEEP=45`,
`SUBMIT_PARALLEL=16`. Torn down and verified gone. About USD 4.

**Verdict: `no-saturation-observed`**, 1274 observations. Read the runner's own
sentence rather than a summary of it: that is not "it scales", it means the
ceiling is above 320 and where it is remains unknown.

**It took three attempts to get one measurement**, and the first two are the
reason this section exists.

| attempt | what happened |
|---|---|
| 1 | gcloud stopped waiting on the TTL roll at 9 of 10 nodes and the script read that as failure, deleted, and could not delete because the operation was still running. Ten nodes left billing (#1206). |
| 2 | provisioned perfectly, then died on an unset `GCP_PROJECT` one line after the cluster came up (#1211). |
| 3 | the run below. |

#### What the cluster held

| | |
|---|---|
| allocatable | **39 200m CPU**, 129.6 GiB memory, 110 pods/node |
| level 320 requested | 32 000m CPU (**82%**), 20.0 GiB (16%), 32 pods/node (29%) |
| `still_pending_at_deadline` | **0 at every level**, including 320 |

Three hundred and twenty pods fit on ten nodes with none of them queuing. CPU
was the tight axis at 82%; memory and pod slots were never close. That margin is
not wide: pods asking 120m instead of 100m would start queuing at this level.

#### The submit phase, and why the earlier number was wrong

An earlier run submitted each level with one `kubectl apply -f` over a
multi-document file, which creates pods **one at a time**. It measured 0.53s per
pod, flat from level 10 to level 320, and that number described the client
rather than the apiserver (#1214).

Submitting across 16 writers:

| level | serial | parallel |
|---|---|---|
| 10 | 0.610 s/pod | **0.183** |
| 40 | 0.551 | **0.105** |
| 320 | 0.530 | **0.082** |
| batch wall at 320 | 169.5 s | **26.2 s** |

Per-pod cost FALLS as the batch grows, which is fixed cost amortizing across
writers, and nothing in the ladder walls.

#### The levels above 40 did not exist before this run

The serial submission took 169s at level 320 while each pod lived 20s, so the
first pods were dead before the last were created. The `start` phase collected
exactly **38 observations at levels 40, 80, 160 and 320** — the size of the
steady-state window, not the level. The nominal ladder was really
`10/20/40/40/40/40`.

`peak_concurrency_observed` exists so that can never again be invisible:

```
level 320 peak_concurrency_observed 320 of 320 nominal
```

All six levels reported full coexistence, and `start` sample counts now track the
level (320 of 320) instead of flatlining at 38.

#### What this run still cannot see

- **The `dispatch` phase.** `--k8s-only` has no leoflow in it, so the scheduler
  tick is ABSENT rather than fast. Three of four candidate ceilings can be
  ranked; the fourth needs a control plane.
- **`schedule` and `start` are quantized.** Kubernetes timestamps are
  whole-second, and both phases resolve to 0 or to 1-versus-2. The runner reports
  `resolution=UNRELIABLE` for them, which is honest and also means they cannot
  rank anything here.
- **`pull` after the first level.** The kubelet caches, so later levels measure
  nothing. `--cold-pull` exists for that and was not used.
- **Level 10's `start` series is 3 of 10.** The readiness gate counts pods in
  `Pending` only, and a pod pulling an image reports `ContainerCreating`, so at
  the smallest level collection raced the cold pull. Higher levels take long
  enough to submit that the race disappears. The low-level samples are thin for
  that reason and not for a reason about leoflow.
- **These pods do nothing.** Each requests 100m and sleeps. This measures the
  DISPATCH path, not the cost of work, so 320 here says nothing about 320 real
  tasks with an engine inside them.

One run is one sample of one afternoon on shared hardware. Two runs that
disagree would be a finding; this is a reading.

### Run record: `netpol`, 2026-09-20, GKE Dataplane V2

Cluster `leoflow-exp-netpol-09192327`, 1 x `e2-standard-4`, `us-central1-a`,
Kubernetes 1.35.7-gke.1222000, `--ttl 40m`, torn down and verified gone.

**Gates, in the order they had to pass before any row was asserted:**

| Gate | Result |
|---|---|
| `datapathProvider` | `ADVANCED_DATAPATH` |
| Dataplane V2 node agent (`anetd`) Ready | 1/1 nodes |
| Baseline, **no policy applied at all** | `169.254.169.254` ALLOWED, `8.8.8.8:53` ALLOWED, `169.254.170.23` **DROPPED** |
| Enforcement, differential | selected=`DROPPED`, control=`ALLOWED` -> **ENFORCING** |

**Rows:**

| Row | Result | Evidence |
|---|---|---|
| **#958** metadata blocked with an empty hatch | **PASS** | baseline ALLOWED -> observed DROPPED |
| **#958** the `/32` hatch restores that one host | **PASS** | observed ALLOWED |
| egress outside the range still works | ALLOWED | so the block is scoped, not a blanket deny |

The enforcement line is the one that matters. `probe-task` (carrying
`leoflow.io/run-id`) lost the metadata server at the same instant `probe-plain`
(carrying nothing) still reached it. That difference is what rules out "the
metadata server went away" and "the node lost egress", neither of which a
single-pod probe can separate from enforcement. On k3d this same assertion
passes with the CNI doing nothing at all.

**What this run could NOT see, and does not claim:**

- **EKS.** §3b names `169.254.170.23` (EKS Pod Identity). On GKE that address
  was **DROPPED at baseline, before any policy existed**, because it does not
  exist on this cloud. Probing it here would "pass" the row while proving
  nothing, so the runner reports it `UNPROVABLE` and the EKS half of #958
  **remains unproven**. Copying the row's literal address into a GKE probe is
  the specific mistake this is guarding against.
- **Calico.** The row asks for three CNIs. One was exercised.
- **A real task pod.** `probe-task` is a busybox pod wearing the label the
  policy selects. This proves the **policy and the CNI**, not that
  `executor/kubernetes.go` puts that label on a real task pod.
- **The additivity concern beyond one host.** Showing that the `/32` hatch does
  not reopen the whole range needs a *second* address in `169.254.0.0/16` that
  answers at baseline. On this cluster there was none. That sub-claim is
  **not re-proven** here.

## Why `warm-pool-ab` has three arms and not two

Turning warm pools on is not one flag. The chart refuses to render without
`auth.agentTokenTransport=exchange` **and** `auth.secretLivenessMode=enforce`
(`helm/dexaflow/templates/deployment.yaml:199-201`), and the server enforces the
same coupling at boot, so an install that moved one would CrashLoopBackOff. The
reason is a real invariant (ADR 0058 D2): a warm pod outlives the attempt it was
created for, so a credential that outlives an attempt would let a superseded
attempt still resolve secrets.

So "warm pools on" is three changes, and one of them, the exchange transport,
adds a TokenReview round trip to **every** pod start, warm or not, pushing the
measured number in the *opposite* direction to the one warm pools move it. A
two-arm A/B across that bundle cannot attribute a difference to warm pools.

Hence:

| Arm | Settings | What it is |
|---|---|---|
| A | `envvar` + `observe` + warm off | today's default posture |
| A' | `exchange` + `enforce` + warm off | the prerequisites **without** the feature |
| B | `exchange` + `enforce` + warm **on** | the feature |

`B vs A'` isolates warm pools, one variable. `B vs A` is what an operator
experiences when they "turn on warm pools", bundle and all. `A' vs A` is the
price of the prerequisites alone, which is the number nobody has. All three are
reported separately and none of them is called "the warm pool speedup" on its
own. The arms are self-tested to differ in exactly the settings they claim to,
and the coupling is checked against the **real chart** across all eight
combinations, so the day it changes this fails locally rather than mid-run.

Arms run A, A', B, **A again**, and the two A measurements are compared. If they
disagree by more than 1.5x the cluster drifted under the experiment and the
comparison is reported `UNTRUSTWORTHY` rather than as a result. Drift in either
direction invalidates: a cluster that got faster flatters whichever arm ran last.

## What is still not here

- **The DAG build-and-push pipeline.** `dexaflow deploy` against
  `test/gcp/dags/gcp_probe/` with a token from the bootstrap admin. Until it
  exists, `warm-pool-ab.sh` cannot run and `pod-per-task.sh` measures only the
  Kubernetes half.
- **The registry decision, made but not automated.** The runners use the
  existing long-lived `leoflow-validate` repository in `us-central1` rather than
  creating one per run. It is the choice this README asked for: a deliberate,
  known line on the bill. Nothing here deletes it, and nothing here can, because
  it carries none of the labels that make deleting a cluster safe.
- **EKS and Calico.** Every row below is GKE Dataplane V2 only.
