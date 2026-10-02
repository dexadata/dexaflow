// Command leoflow-agent runs as PID 1 inside every task pod. It connects back to
// the control plane over gRPC, fetches the task spec, runs the user process while
// streaming logs, pushes the return value, and reports the terminal state.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/dexadata/dexaflow/internal/agent"
	"github.com/dexadata/dexaflow/internal/envcompat"
	"github.com/dexadata/dexaflow/internal/version"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// usage is printed for `--help`. dexaflow-agent takes no positional args; it is
// configured via environment and normally launched by the control plane.
const usage = `dexaflow-agent — runs as PID 1 inside a task pod: connects to the control plane
over gRPC, runs the task, streams logs, and reports the result.

Configured via environment (DEXAFLOW_CONTROL_PLANE_ADDR, DEXAFLOW_AGENT_TOKEN, …);
there are no positional arguments. Normally launched by the control plane, not
by hand.

Flags:
  --version   print version and exit
  --help, -h  print this help and exit
`

func main() {
	// The control plane passes LEOFLOW_* to task pods, and older control planes
	// only know that name; mirror it so DEXAFLOW_* readers see the same values.
	// A conflict is reported, the plain legacy name is the normal case here.
	for _, note := range envcompat.MirrorProcess().ConflictNotes() {
		fmt.Fprintln(os.Stderr, "dexaflow-agent: "+note)
	}
	os.Exit(run())
}

// bootstrapToken resolves the agent's initial bearer and whether a token exchange
// is required (ADR 0055 Fix #3). Under the exchange transport it reads the
// projected ServiceAccount token from LEOFLOW_AGENT_TOKEN_PATH (the bootstrap
// credential to exchange); otherwise it returns LEOFLOW_AGENT_TOKEN unchanged
// (the env-var default). exchange is true only when both the transport marker and
// the token path are set.
func bootstrapToken() (token string, exchange bool, err error) {
	if os.Getenv("LEOFLOW_AGENT_TOKEN_TRANSPORT") == "exchange" {
		if path := os.Getenv("LEOFLOW_AGENT_TOKEN_PATH"); path != "" {
			projected, rerr := agent.ReadTokenFile(path)
			if rerr != nil {
				return "", true, rerr
			}
			return projected, true, nil
		}
	}
	return os.Getenv("LEOFLOW_AGENT_TOKEN"), false, nil
}

// envInt reads a non-negative integer env var, returning 0 when unset or
// unparseable. The warm-worker caps treat 0 as "no bound", so a missing or
// malformed value disables that cap rather than failing startup.
func envInt(key string) int {
	n, err := strconv.Atoi(os.Getenv(key))
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// envSeconds reads an integer-seconds env var into a Duration, returning 0 (=
// "no bound") when unset or unparseable, matching envInt's fail-open-to-disabled
// convention for the warm-worker lifecycle caps (ADR 0058 N1d-d).
func envSeconds(key string) time.Duration {
	return time.Duration(envInt(key)) * time.Second
}

// configureRunnerHooks wires the external-secrets resolver (ADR 0060) from the
// operator-injected LEOFLOW_SECRETS_* pod env and the durable-outcome chaos seam
// (ADR 0052) onto the runner. A malformed secrets backend config is a hard error
// (fail closed); with none configured the secret chain stays vault-only. The chaos
// seam is dev-only.
func configureRunnerHooks(runner *agent.Runner) error {
	resolver, backend, err := agent.ResolverFromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("external secrets backend config is invalid: %w", err)
	}
	runner.Resolver, runner.SecretBackend = resolver, backend
	if want := os.Getenv("LEOFLOW_CHAOS_DIE_BEFORE_REPORT"); want != "" {
		runner.BeforeReport = func(state agentv1.TaskState) {
			if state.String() == want {
				slog.Warn("chaos: exiting before report to simulate a pod killed mid-report", "state", state.String())
				os.Exit(137)
			}
		}
	}
	return nil
}

func run() int {
	// Answer `--version`/`--help` before connecting, so an operator can query a
	// deployed agent without a reachable control plane (#593). Without this,
	// `--help` falls through to a Dial that errors on a missing address.
	args := os.Args[1:]
	switch {
	case version.WantsVersion(args):
		fmt.Println(version.Get().String())
		return 0
	case version.WantsHelp(args):
		fmt.Print(usage)
		return 0
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	addr := os.Getenv("LEOFLOW_CONTROL_PLANE_ADDR")
	allowInsecure := os.Getenv("LEOFLOW_AGENT_INSECURE") != "false"
	caFile := os.Getenv("LEOFLOW_AGENT_TLS_CA") // PEM CA to verify the server cert (TLS)

	// Bootstrap credential: under the exchange transport (ADR 0055 Fix #3) the
	// control plane projects a ServiceAccount token at LEOFLOW_AGENT_TOKEN_PATH,
	// which the agent exchanges once for a task-scoped JWT after dialing. The
	// default env-var transport leaves the path unset and uses LEOFLOW_AGENT_TOKEN
	// directly (byte-identical to before).

	// Every exit below happens BEFORE the agent registers with the control plane,
	// so nothing it logs here is ever shipped and the task streams no logs at all.
	// The container termination message is the only channel left: a classified
	// reason written there reaches the reconciler, which records it against the
	// attempt. Without it the operator sees "failed" with no cause anywhere.
	terminationLog := os.Getenv("LEOFLOW_TERMINATION_LOG_PATH")

	token, exchange, berr := bootstrapToken()
	if berr != nil {
		slog.Error("reading projected bootstrap token", "error", berr)
		agent.ReportBootstrapFailure(terminationLog, agent.StageToken, berr)
		return 1
	}

	client, conn, tokens, err := agent.Dial(addr, token, allowInsecure, caFile)
	if err != nil {
		slog.Error("connecting to control plane", "error", err)
		agent.ReportBootstrapFailure(terminationLog, agent.StageDial, err)
		return 1
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			slog.Warn("closing connection", "error", cerr)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Warm-worker mode (ADR 0058): a long-lived process that registers once with
	// the bootstrap token and serves MANY attempts over the AwaitAssignment stream,
	// each in a fresh forked child. Selected by env; the bootstrap credential comes
	// from the same path as single-shot. Anything else falls through to the
	// byte-identical single-shot path below.
	if os.Getenv("LEOFLOW_WARM_WORKER") == "1" {
		return runWarm(ctx, client, tokens, exchange, addr, token, allowInsecure, caFile)
	}

	// Exchange the projected bootstrap token for the task-scoped JWT before any
	// other RPC, then swap it into the shared TokenSource so every subsequent call
	// authenticates as the task instance. Fails startup if the exchange is refused.
	if exchange {
		if xerr := agent.ExchangeToken(ctx, client, tokens); xerr != nil {
			slog.Error("exchanging bootstrap token for a task-scoped credential", "error", xerr)
			agent.ReportBootstrapFailure(terminationLog, agent.StageExchange, xerr)
			return 1
		}
	}

	sink, err := agent.OpenLogSink(ctx, client)
	if err != nil {
		slog.Warn("log streaming unavailable; logs will not be shipped this run", "error", err)
		sink = agent.NoopLogSink{}
	}

	hostname, herr := os.Hostname()
	if herr != nil {
		hostname = "unknown"
	}

	returnPath, cleanupReturn, rverr := agent.NewReturnValuePath()
	if rverr != nil {
		slog.Error("preparing return-value path", "error", rverr)
		return 1
	}
	defer func() { _ = cleanupReturn() }() //nolint:errcheck // best-effort cleanup of the per-task temp dir on exit

	runner := &agent.Runner{
		Client:     client,
		Cmd:        agent.NewExecRunner(),
		Sink:       sink,
		Hostname:   hostname,
		Version:    version.Get().Version,
		Env:        os.Environ(),
		ReturnPath: returnPath,
		// Operator extra-links share the return value's per-task temp dir (#375).
		LinksPath: filepath.Join(filepath.Dir(returnPath), "extra_links.json"),
		// Custom-keyed XCom pushes, same per-task temp dir (multi-key XCom).
		PushesPath: filepath.Join(filepath.Dir(returnPath), "xcom_pushes.json"),
		// A reschedule-mode sensor's next-poke time, same per-task temp dir (#380).
		ReschedulePath: filepath.Join(filepath.Dir(returnPath), "reschedule.txt"),
		// The durable outcome record's destination (the container termination
		// message), set by the executor's podEnv; empty outside a pod (ADR 0052).
		TerminationLogPath: os.Getenv("LEOFLOW_TERMINATION_LOG_PATH"),
		HeartbeatInterval:  agent.DefaultHeartbeatInterval,
		// The heartbeat loop swaps a renewed bearer into this source (ADR 0055
		// Fix #4); the per-RPC credential reads it on every subsequent call.
		Token: tokens,
	}
	if herr := configureRunnerHooks(runner); herr != nil {
		slog.Error("configuring runner", "error", herr)
		return 1
	}
	if rerr := runner.Run(ctx); rerr != nil {
		slog.Error("task failed", "error", rerr)
		return 1
	}
	return 0
}

// runWarm serves the warm-worker loop (ADR 0058). streamClient carries the
// worker's bootstrap identity (Register + AwaitAssignment). streamTokens is that
// stream's bearer source: under the exchange transport it seeds the projected
// ServiceAccount token, which the warm loop swaps for a WORKER-scoped JWT (before
// Register, on every connect) so the control channel authenticates as a warm
// worker that authorizes ONLY Register + AwaitAssignment (never secrets/task).
//
// A SECOND dial gives the WorkClient its own connection bound to an attempt-scoped
// TokenSource, which the loop swaps to each assignment's attempt_token — so the
// per-attempt work RPCs authenticate as the attempt while the stream keeps the
// worker identity it was opened with. The work dial is seeded with the bootstrap
// token only to satisfy Dial's non-empty check; no work RPC fires before the first
// attempt_token swap, and the per-attempt flow is unchanged by the exchange.
func runWarm(ctx context.Context, streamClient agentv1.AgentServiceClient, streamTokens *agent.TokenSource, exchange bool, addr, seedToken string, allowInsecure bool, caFile string) int {
	dagVersionID := os.Getenv("LEOFLOW_DAG_VERSION_ID")
	if dagVersionID == "" {
		slog.Error("warm worker requires LEOFLOW_DAG_VERSION_ID")
		return 1
	}

	workClient, workConn, attemptTokens, err := agent.Dial(addr, seedToken, allowInsecure, caFile)
	if err != nil {
		slog.Error("dialing control plane for per-attempt work", "error", err)
		return 1
	}
	defer func() {
		if cerr := workConn.Close(); cerr != nil {
			slog.Warn("closing work connection", "error", cerr)
		}
	}()

	hostname, herr := os.Hostname()
	if herr != nil {
		hostname = "unknown"
	}

	// The agent-owned scratch root, wiped and recreated before every attempt
	// (D4 isolation). Removed on exit.
	scratchDir, err := os.MkdirTemp("", "leoflow-warm-")
	if err != nil {
		slog.Error("preparing warm scratch dir", "error", err)
		return 1
	}
	defer func() { _ = os.RemoveAll(scratchDir) }() //nolint:errcheck // best-effort cleanup on exit

	w := &agent.WarmRunner{
		StreamClient:  streamClient,
		WorkClient:    workClient,
		AttemptTokens: attemptTokens,
		// Under the exchange transport the stream bearer starts as the projected SA
		// token; ExchangeBootstrap swaps a worker-scoped JWT into it before Register.
		StreamTokens:      streamTokens,
		ExchangeBootstrap: exchange,
		// A fresh per-attempt log sink on the work client, so each attempt's logs
		// ship under its own attempt_token.
		NewSink: func(ctx context.Context) (agent.LogSink, error) {
			return agent.OpenLogSink(ctx, workClient)
		},
		Cmd:      agent.NewExecRunner(),
		Hostname: hostname,
		Version:  version.Get().Version,
		Env:      os.Environ(),
		// The pod's own name, injected by BuildWarmPod via the downward API. Sent
		// up in WorkerRegister so the control plane binds a started attempt to it
		// as the durable warm_worker_id (ADR 0058 N1d-a1). Empty off-cluster.
		PodName:            os.Getenv("LEOFLOW_POD_NAME"),
		ScratchDir:         scratchDir,
		TerminationLogPath: os.Getenv("LEOFLOW_TERMINATION_LOG_PATH"),
		HeartbeatInterval:  agent.DefaultHeartbeatInterval,
		// Warm-pool Hole B: on a not-leader rejection, re-dial the STREAM connection
		// so a fresh Service backend can reach the scheduler leader (a single gRPC
		// connection sticks to one backend). The work connection is untouched. The
		// returned conn is closed by WarmRunner on the next reconnect / exit, and the
		// returned TokenSource becomes the new stream bearer — under the exchange
		// transport WarmRunner re-runs the exchange into it before Register, so a
		// reconnect gets a FRESH worker JWT (the initial one may have lapsed). The
		// projected token is re-read from disk so a kubelet-rotated token is honored.
		Redial: func() (agentv1.AgentServiceClient, *agent.TokenSource, io.Closer, error) {
			seed := seedToken
			if exchange {
				if path := os.Getenv("LEOFLOW_AGENT_TOKEN_PATH"); path != "" {
					if fresh, rerr := agent.ReadTokenFile(path); rerr == nil {
						seed = fresh
					} else {
						slog.Warn("re-reading projected token on reconnect; falling back to the startup token", "error", rerr)
					}
				}
			}
			c, conn, newTokens, derr := agent.Dial(addr, seed, allowInsecure, caFile)
			if derr != nil {
				return nil, nil, nil, derr
			}
			return c, newTokens, conn, nil
		},
		// Self-lifecycle caps, injected by BuildWarmPod from the operator's
		// execution.* config (ADR 0058 N1d-d). Zero (unset/invalid) disables that
		// bound in WarmRunner, so an off-cluster run is unbounded exactly as before.
		MaxAttempts:     envInt("LEOFLOW_MAX_ATTEMPTS_PER_WORKER"),
		MaxLifetime:     envSeconds("LEOFLOW_MAX_WORKER_LIFETIME_SECONDS"),
		IdleTTL:         envSeconds("LEOFLOW_WORKER_IDLE_TTL_SECONDS"),
		AttemptWatchdog: envSeconds("LEOFLOW_ATTEMPT_WATCHDOG_SECONDS"),
		// Read-only root isolation (X3.2), set by BuildWarmPod: a per-attempt HOME
		// under the scratch, and a sweep of the /tmp emptyDir the scratch lives in.
		AttemptHome:  os.Getenv("LEOFLOW_WARM_ATTEMPT_HOME") == "1",
		SharedTmpDir: filepath.Dir(scratchDir),
	}
	if rerr := w.Run(ctx, dagVersionID); rerr != nil {
		slog.Error("warm worker failed", "error", rerr)
		return 1
	}
	return 0
}
