package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeClient is a test double for the generated AgentServiceClient.
type fakeClient struct {
	spec        *agentv1.TaskSpec
	xcom        map[string]*agentv1.FetchXComResponse
	states      []agentv1.TaskState
	reports     []*agentv1.ReportStateRequest
	pushed      []*agentv1.PushXComRequest
	registered  bool
	terminateAt agentv1.TaskState // state for which ReportState returns should_terminate
	getSpecErr  error
	pushErr     error
	// fetchXComErr, when set, makes every FetchXCom fail with it — the
	// environment-build failure path that is NOT a clean NotFound miss.
	fetchXComErr error
	// reportFailCode + reportFailTimes make the first reportFailTimes ReportState
	// calls fail with that gRPC code, then succeed — to exercise the report retry.
	reportFailCode  codes.Code
	reportFailTimes int
	reportAttempts  int
	// failReportState, when set, makes ReportState fail for that state only (e.g.
	// fail the terminal SUCCESS report but let the earlier RUNNING report land) —
	// used to prove the outcome record is durable even when its report is lost.
	failReportState    agentv1.TaskState
	heartbeatTerminate bool
	vars               map[string]string
	conns              map[string]string
	getVarsErr         error
	getConnsErr        error
}

func (f *fakeClient) GetVariables(context.Context, *agentv1.GetVariablesRequest, ...grpc.CallOption) (*agentv1.GetVariablesResponse, error) {
	if f.getVarsErr != nil {
		return nil, f.getVarsErr
	}
	return &agentv1.GetVariablesResponse{Variables: f.vars}, nil
}

func (f *fakeClient) GetConnections(context.Context, *agentv1.GetConnectionsRequest, ...grpc.CallOption) (*agentv1.GetConnectionsResponse, error) {
	if f.getConnsErr != nil {
		return nil, f.getConnsErr
	}
	return &agentv1.GetConnectionsResponse{ConnectionUris: f.conns}, nil
}

func (f *fakeClient) ExchangeToken(context.Context, *agentv1.ExchangeTokenRequest, ...grpc.CallOption) (*agentv1.ExchangeTokenResponse, error) {
	return &agentv1.ExchangeTokenResponse{AgentToken: "exchanged"}, nil
}

func (f *fakeClient) Register(context.Context, *agentv1.RegisterRequest, ...grpc.CallOption) (*agentv1.RegisterResponse, error) {
	f.registered = true
	return &agentv1.RegisterResponse{SessionId: "s1"}, nil
}

func (f *fakeClient) GetTaskSpec(context.Context, *agentv1.GetTaskSpecRequest, ...grpc.CallOption) (*agentv1.TaskSpec, error) {
	if f.getSpecErr != nil {
		return nil, f.getSpecErr
	}
	return f.spec, nil
}

// AwaitAssignment satisfies the AgentServiceClient interface (warm-worker
// transport, ADR 0058 N1b). No single-shot agent test drives it; the embedding
// fakes (exchangeClient, registerErrClient, secretsErrClient) inherit this stub.
func (f *fakeClient) AwaitAssignment(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[agentv1.WorkerMessage, agentv1.WorkAssignment], error) {
	return nil, status.Error(codes.Unimplemented, "AwaitAssignment not used in this test")
}

func (f *fakeClient) FetchXCom(_ context.Context, in *agentv1.FetchXComRequest, _ ...grpc.CallOption) (*agentv1.FetchXComResponse, error) {
	if f.fetchXComErr != nil {
		return nil, f.fetchXComErr
	}
	if resp, ok := f.xcom[in.GetUpstreamTaskId()]; ok {
		return resp, nil
	}
	return nil, status.Error(codes.NotFound, "no xcom for "+in.GetUpstreamTaskId())
}

func (f *fakeClient) PushXCom(_ context.Context, in *agentv1.PushXComRequest, _ ...grpc.CallOption) (*agentv1.PushXComResponse, error) {
	f.pushed = append(f.pushed, in)
	if f.pushErr != nil {
		return nil, f.pushErr
	}
	return &agentv1.PushXComResponse{Accepted: true}, nil
}

func (f *fakeClient) StreamLogs(context.Context, ...grpc.CallOption) (grpc.BidiStreamingClient[agentv1.LogLine, agentv1.LogAck], error) {
	return nil, errors.New("not used in these tests")
}

func (f *fakeClient) ReportState(_ context.Context, in *agentv1.ReportStateRequest, _ ...grpc.CallOption) (*agentv1.ReportStateResponse, error) {
	f.reportAttempts++
	if f.reportAttempts <= f.reportFailTimes {
		return nil, status.Error(f.reportFailCode, "injected report failure")
	}
	if f.failReportState != agentv1.TaskState_TASK_STATE_UNSPECIFIED && in.GetState() == f.failReportState {
		return nil, status.Error(codes.Internal, "injected terminal report failure")
	}
	f.states = append(f.states, in.GetState())
	f.reports = append(f.reports, in)
	return &agentv1.ReportStateResponse{Acknowledged: true, ShouldTerminate: in.GetState() == f.terminateAt}, nil
}

func (f *fakeClient) Heartbeat(context.Context, *agentv1.HeartbeatRequest, ...grpc.CallOption) (*agentv1.HeartbeatResponse, error) {
	return &agentv1.HeartbeatResponse{ShouldTerminate: f.heartbeatTerminate}, nil
}

// recordingSink captures log lines instead of streaming them.
type recordingSink struct {
	lines  []string
	closed bool
}

func (s *recordingSink) Send(line *agentv1.LogLine) error {
	s.lines = append(s.lines, line.GetMessage())
	return nil
}
func (s *recordingSink) Close() error { s.closed = true; return nil }

// fakeCmd is a CommandRunner double that records its inputs and emits output.
type fakeCmd struct {
	argv             []string
	env              []string
	stdout           string
	exitCode         int
	err              error
	blockUntilCancel bool
}

func (c *fakeCmd) Run(ctx context.Context, argv, env []string, stdout, _ io.Writer) (int, error) {
	c.argv, c.env = argv, env
	if c.stdout != "" {
		_, _ = io.WriteString(stdout, c.stdout)
	}
	if c.blockUntilCancel {
		<-ctx.Done()
		return 137, ctx.Err()
	}
	return c.exitCode, c.err
}

func newRunner(client *fakeClient, cmd *fakeCmd, sink *recordingSink) *Runner {
	return &Runner{
		Client:   client,
		Cmd:      cmd,
		Sink:     sink,
		Hostname: "pod-1",
		Version:  "test",
	}
}

func TestRunnerHappyPath(t *testing.T) {
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:         "python",
			Entrypoint:       "dag:hello",
			Environment:      map[string]string{"FOO": "bar"},
			XcomInputMapping: map[string]*agentv1.XComUpstreams{"upstream_val": {TaskIds: []string{"extract"}}},
		},
		xcom: map[string]*agentv1.FetchXComResponse{
			"extract": {Value: []byte(`{"n":1}`)},
		},
	}
	cmd := &fakeCmd{stdout: "line one\nline two\n"}
	sink := &recordingSink{}
	r := newRunner(client, cmd, sink)

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !client.registered {
		t.Error("agent should register on startup")
	}
	wantStates := []agentv1.TaskState{agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_SUCCESS}
	if len(client.states) != 2 || client.states[0] != wantStates[0] || client.states[1] != wantStates[1] {
		t.Errorf("states = %v, want running then success", client.states)
	}
	if cmd.argv[0] != "python" {
		t.Errorf("argv = %v, want python command", cmd.argv)
	}
	joined := strings.Join(cmd.env, "\n")
	if !strings.Contains(joined, "FOO=bar") {
		t.Errorf("env missing spec var: %v", cmd.env)
	}
	if !strings.Contains(joined, `LEOFLOW_XCOM_UPSTREAM_VAL={"n":1}`) {
		t.Errorf("env missing xcom input: %v", cmd.env)
	}
	// Variables/Connections are exported as Airflow env secrets.
	client.vars = map[string]string{"my_var": "v1"}
	client.conns = map[string]string{"my_db": "postgres://u:p@h/db"}
	env, err := r.buildEnv(context.Background(), client.spec)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	je := strings.Join(env, "\n")
	if !strings.Contains(je, "AIRFLOW_VAR_MY_VAR=v1") {
		t.Errorf("env missing AIRFLOW_VAR_MY_VAR: %v", env)
	}
	if !strings.Contains(je, "AIRFLOW_CONN_MY_DB=postgres://u:p@h/db") {
		t.Errorf("env missing AIRFLOW_CONN_MY_DB: %v", env)
	}
	// The agent frames a run with synthetic start/end events (#119) and emits
	// task-boot lifecycle lines (running:, injected ...). With the upstream
	// XCom this fixture sets, the captured stream is:
	//   ["▸ task started", "running: ...", "injected ...", "line one", "line two", "✓ task succeeded ..."]
	if len(sink.lines) != 6 {
		t.Fatalf("log lines = %v (len=%d), want 6 (framing+boot+2lifecycle+2 lines+ framing)", sink.lines, len(sink.lines))
	}
	if !strings.Contains(sink.lines[0], "task started") {
		t.Errorf("line[0] must be the start framing, got %q", sink.lines[0])
	}
	if !strings.HasPrefix(sink.lines[1], "running:") {
		t.Errorf("line[1] must be the boot argv line, got %q", sink.lines[1])
	}
	if !strings.HasPrefix(sink.lines[2], "injected ") {
		t.Errorf("line[2] must be the injected env line, got %q", sink.lines[2])
	}
	if sink.lines[3] != "line one" || sink.lines[4] != "line two" {
		t.Errorf("lines[3..4] should be the user stdout 'line one' / 'line two', got %q / %q", sink.lines[3], sink.lines[4])
	}
	if !strings.Contains(sink.lines[5], "succeeded") {
		t.Errorf("last line must be the success framing, got %q", sink.lines[5])
	}
	if !sink.closed {
		t.Error("log sink should be closed after the command exits")
	}
}

func TestBuildEnvStampsRunContext(t *testing.T) {
	// The runtime's standalone operator context (_StandaloneTaskInstance / ADR 0040)
	// reads LEOFLOW_TASK_ID/RUN_ID/TRY_NUMBER/DAG_ID. The server already sends these
	// on the TaskSpec; the agent must stamp them into the process env, else the
	// context is blank in every executor (silent-wrong for operators that read them).
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator: "airflow_operator", DagId: "etl", RunId: "run-123",
		TaskId: "load", TryNumber: 3,
	}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})

	env, err := r.buildEnv(context.Background(), client.spec)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	je := strings.Join(env, "\n")
	for _, want := range []string{
		"LEOFLOW_TASK_ID=load", "LEOFLOW_RUN_ID=run-123",
		"LEOFLOW_DAG_ID=etl", "LEOFLOW_TRY_NUMBER=3",
	} {
		if !strings.Contains(je, want) {
			t.Errorf("env missing %q; got %v", want, env)
		}
	}
}

func TestBuildEnvStampsParams(t *testing.T) {
	// The DagRun's params/conf reach the runtime's context['params'] via LEOFLOW_PARAMS (#148).
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator:   "airflow_operator",
		ParamsJson: `{"region":"us-east1"}`,
	}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})

	env, err := r.buildEnv(context.Background(), client.spec)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	if !strings.Contains(strings.Join(env, "\n"), `LEOFLOW_PARAMS={"region":"us-east1"}`) {
		t.Errorf("env missing LEOFLOW_PARAMS; got %v", env)
	}
}

func TestBuildEnvStampsDataInterval(t *testing.T) {
	// The runtime's standalone context exposes data_interval_start/end; the agent
	// stamps them from the DagRun's interval the server sends (ADR 0040).
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator:          "airflow_operator",
		DataIntervalStart: "2026-06-13T00:00:00Z",
		DataIntervalEnd:   "2026-06-14T00:00:00Z",
	}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})

	env, err := r.buildEnv(context.Background(), client.spec)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	je := strings.Join(env, "\n")
	for _, want := range []string{
		"LEOFLOW_DATA_INTERVAL_START=2026-06-13T00:00:00Z",
		"LEOFLOW_DATA_INTERVAL_END=2026-06-14T00:00:00Z",
	} {
		if !strings.Contains(je, want) {
			t.Errorf("env missing %q; got %v", want, env)
		}
	}
}

func TestBuildEnvDerivesDsTsFromLogicalDate(t *testing.T) {
	// The runtime's standalone context exposes ds/ts (LEOFLOW_DS / LEOFLOW_TS). The
	// agent derives both from the DagRun's logical_date the server sends — ts is the
	// RFC3339 value, ds its UTC date — so date-filtering operators see a real value
	// instead of "".
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator: "airflow_operator", LogicalDate: "2026-06-13T08:30:00Z",
	}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})

	env, err := r.buildEnv(context.Background(), client.spec)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	je := strings.Join(env, "\n")
	for _, want := range []string{"LEOFLOW_TS=2026-06-13T08:30:00Z", "LEOFLOW_DS=2026-06-13"} {
		if !strings.Contains(je, want) {
			t.Errorf("env missing %q; got %v", want, env)
		}
	}
}

func TestBuildEnvDeliversXComByTaskForOperators(t *testing.T) {
	// For a captured operator (ADR 0040), the agent fetches each upstream's
	// return_value and delivers them as the LEOFLOW_UPSTREAM_XCOM map so the
	// runtime's ti.xcom_pull('compile') resolves it — chained operators like
	// Airflow. A missing upstream is omitted (pulls as None).
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:      "airflow_operator",
			OperatorClass: "x.Y",
			DependsOn:     []string{"compile", "missing"},
		},
		xcom: map[string]*agentv1.FetchXComResponse{
			"compile": {Value: []byte(`{"name":"abc"}`)},
		},
	}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})

	env, err := r.buildEnv(context.Background(), client.spec)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	je := strings.Join(env, "\n")
	if !strings.Contains(je, `LEOFLOW_UPSTREAM_XCOM={"compile":{"name":"abc"}}`) {
		t.Errorf("env missing/!= xcom-by-task map; got %v", env)
	}
}

func TestBuildEnvSkipsXComByTaskForNonOperators(t *testing.T) {
	// A python @task gets its inputs via xcom_input_mapping, not ti.xcom_pull, so
	// the agent must not waste fetches building the map for it.
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator: "python", Entrypoint: "dag:f", DependsOn: []string{"compile"},
	}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})

	env, err := r.buildEnv(context.Background(), client.spec)
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	if strings.Contains(strings.Join(env, "\n"), "LEOFLOW_UPSTREAM_XCOM") {
		t.Errorf("non-operator task should not get the xcom-by-task map; got %v", env)
	}
}

func TestRunnerPushesCustomXComs(t *testing.T) {
	// The runtime writes the operator's custom-keyed ti.xcom_push values to PushesPath;
	// the agent stores each as its own XCom (multi-key XCom — visible in the XCom tab).
	pushes := filepath.Join(t.TempDir(), "pushes.json")
	if err := os.WriteFile(pushes, []byte(`{"row_count":42,"summary":{"ok":true}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "airflow_operator", OperatorClass: "x.Y"}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.PushesPath = pushes

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := map[string]string{}
	for _, p := range client.pushed {
		got[p.GetKey()] = string(p.GetValue())
	}
	if got["row_count"] != "42" || got["summary"] != `{"ok":true}` {
		t.Errorf("custom xcoms pushed = %v", got)
	}
}

func TestRunnerPushesExtraLinks(t *testing.T) {
	// The runtime computes operator_extra_links and writes them to LinksPath; the
	// agent ships them to the control plane as the reserved "_extra_links" XCom so
	// the UI can render the provider deep-link buttons (#375).
	links := filepath.Join(t.TempDir(), "extra_links.json")
	body := `{"BigQuery Job Detail":"https://console.cloud.google.com/bigquery?x=1"}`
	if err := os.WriteFile(links, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "airflow_operator", OperatorClass: "x.Y"}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.LinksPath = links

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var found *agentv1.PushXComRequest
	for _, p := range client.pushed {
		if p.GetKey() == "_extra_links" {
			found = p
		}
	}
	if found == nil {
		t.Fatalf("no _extra_links PushXCom; pushed=%v", client.pushed)
	}
	if string(found.GetValue()) != body {
		t.Errorf("links value = %s, want %s", found.GetValue(), body)
	}
}

func TestRunnerReportsFailureOnNonZeroExit(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "bash", Entrypoint: "exit 1"}}
	cmd := &fakeCmd{exitCode: 1}
	r := newRunner(client, cmd, &recordingSink{})

	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected an error when the task exits non-zero")
	}
	last := client.states[len(client.states)-1]
	if last != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want failed", last)
	}
}

func TestRunnerPushesReturnValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "return_value.json")
	if err := os.WriteFile(path, []byte(`{"result":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:f"}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.ReturnPath = path

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(client.pushed) != 1 {
		t.Fatalf("expected one PushXCom, got %d", len(client.pushed))
	}
	if string(client.pushed[0].GetValue()) != `{"result":42}` {
		t.Errorf("pushed value = %s", client.pushed[0].GetValue())
	}
}

func TestRunnerToleratesUnimplementedPush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "return_value.json")
	if err := os.WriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{
		spec:    &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:f"},
		pushErr: status.Error(codes.Unimplemented, "xcom not implemented yet"),
	}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.ReturnPath = path

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("an Unimplemented push must not fail the task: %v", err)
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("final state = %v, want success", last)
	}
}

func TestRunnerFailsOnRealPushError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "return_value.json")
	if err := os.WriteFile(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{
		spec:    &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:f"},
		pushErr: status.Error(codes.Internal, "boom"),
	}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	r.ReturnPath = path

	if err := r.Run(context.Background()); err == nil {
		t.Error("a non-Unimplemented push error should fail the task")
	}
}

func TestRunnerHeartbeatCancelsOnTerminate(t *testing.T) {
	client := &fakeClient{
		spec:               &agentv1.TaskSpec{Operator: "bash", Entrypoint: "sleep 1000"},
		heartbeatTerminate: true,
	}
	cmd := &fakeCmd{blockUntilCancel: true}
	r := newRunner(client, cmd, &recordingSink{})
	r.HeartbeatInterval = 5 * time.Millisecond

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a terminated task should fail")
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want failed after termination", last)
	}
}

// TestRunnerStopsOnReportStateTerminate covers the #474 kill switch on the
// report path: when the control plane answers a state report with
// should_terminate (the reporting attempt no longer matches the live row — a
// reaper settled it, or a retry moved past it), the agent must abort rather than
// keep running abandoned work. The very first report is RUNNING, so a terminate
// there stops the task before the user process ever runs.
func TestRunnerStopsOnReportStateTerminate(t *testing.T) {
	client := &fakeClient{
		spec:        &agentv1.TaskSpec{Operator: "bash", Entrypoint: "sleep 1000"},
		terminateAt: agentv1.TaskState_TASK_STATE_RUNNING,
	}
	cmd := &fakeCmd{blockUntilCancel: true}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a task told to terminate on its RUNNING report must not proceed")
	}
	// The user command must never have been invoked: terminate fired on the
	// RUNNING report, before execute() runs the process.
	if cmd.argv != nil {
		t.Errorf("user command ran despite terminate signal: argv=%v", cmd.argv)
	}
}

// TestRunnerEnforcesExecutionTimeout covers issue #194: the pod-path agent must
// stop a task that exceeds its declared execution_timeout_seconds, not rely on
// the scheduler's 90 s heartbeat reaper (which leaves the wedged user code
// running until the agent itself notices). When the timeout fires, the failure
// is reported with a clear "execution_timeout" message so the operator can
// distinguish it from a crash or an exit-code failure.
func TestRunnerEnforcesExecutionTimeout(t *testing.T) {
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:                "bash",
			Entrypoint:              "sleep 1000",
			ExecutionTimeoutSeconds: 1, // proto carries seconds; the implementation uses time.Duration
		},
	}
	cmd := &fakeCmd{blockUntilCancel: true}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err == nil {
		t.Fatal("a task that exceeds its execution_timeout_seconds must fail")
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want failed after timeout", last)
	}
	// The terminal report must name the failure as a timeout (not "task exited
	// non-zero"), so the user can tell a hang from a crash.
	if n := len(client.reports); n > 0 {
		got := client.reports[n-1].GetErrorMessage()
		if !strings.Contains(got, "execution_timeout") {
			t.Errorf("error message should name execution_timeout, got %q", got)
		}
	}
}

// TestRunnerHonorsZeroTimeout keeps the existing semantics for tasks with no
// declared timeout (0): no time bound, the task runs until it finishes or the
// agent process itself is canceled.
func TestRunnerHonorsZeroTimeout(t *testing.T) {
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator:                "python",
			Entrypoint:              "dag:fast",
			ExecutionTimeoutSeconds: 0, // unset → no timeout applies
		},
	}
	cmd := &fakeCmd{exitCode: 0}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("zero execution_timeout_seconds must not interfere with a fast task: %v", err)
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("final state = %v, want success", last)
	}
}

func TestRunnerSkipsAbsentXComInput(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{
		Operator: "python", Entrypoint: "dag:f",
		XcomInputMapping: map[string]*agentv1.XComUpstreams{"maybe": {TaskIds: []string{"upstream"}}}, // upstream pushed nothing
	}}
	cmd := &fakeCmd{}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("an absent declared xcom input must not fail the task: %v", err)
	}
	if strings.Contains(strings.Join(cmd.env, "\n"), "LEOFLOW_XCOM_MAYBE") {
		t.Errorf("absent xcom input should be skipped (None), not set: %v", cmd.env)
	}
}

func TestRunnerFanInCollectsUpstreamsIntoJSONArray(t *testing.T) {
	// Fan-in: `combine([part() for _ in range(3)])` declares 3 upstreams under
	// one parameter. The agent must fetch each and assemble a JSON array, in
	// declaration order, for the runtime to deliver as the function's list arg.
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator: "python", Entrypoint: "dag:combine",
			XcomInputMapping: map[string]*agentv1.XComUpstreams{
				"parts": {TaskIds: []string{"part", "part__1", "part__2"}},
			},
		},
		xcom: map[string]*agentv1.FetchXComResponse{
			"part":    {Value: []byte(`{"i":0}`)},
			"part__1": {Value: []byte(`{"i":1}`)},
			"part__2": {Value: []byte(`{"i":2}`)},
		},
	}
	cmd := &fakeCmd{}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got string
	for _, kv := range cmd.env {
		if strings.HasPrefix(kv, "LEOFLOW_XCOM_PARTS=") {
			got = strings.TrimPrefix(kv, "LEOFLOW_XCOM_PARTS=")
		}
	}
	want := `[{"i":0},{"i":1},{"i":2}]`
	if got != want {
		t.Errorf("fan-in PARTS env: got %q, want %q", got, want)
	}
}

func TestRunnerFanInAbsentUpstreamGetsNull(t *testing.T) {
	// A missing upstream contributes `null` so the function still receives the
	// right element count — matches Airflow's "missing XCom resolves to None"
	// semantics without dropping the index.
	client := &fakeClient{
		spec: &agentv1.TaskSpec{
			Operator: "python", Entrypoint: "dag:combine",
			XcomInputMapping: map[string]*agentv1.XComUpstreams{
				"parts": {TaskIds: []string{"a", "b", "c"}},
			},
		},
		xcom: map[string]*agentv1.FetchXComResponse{
			"a": {Value: []byte(`1`)},
			"c": {Value: []byte(`3`)},
			// b is missing — agent should emit null in slot 1.
		},
	}
	cmd := &fakeCmd{}
	r := newRunner(client, cmd, &recordingSink{})

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	var got string
	for _, kv := range cmd.env {
		if strings.HasPrefix(kv, "LEOFLOW_XCOM_PARTS=") {
			got = strings.TrimPrefix(kv, "LEOFLOW_XCOM_PARTS=")
		}
	}
	want := `[1,null,3]`
	if got != want {
		t.Errorf("fan-in PARTS env with one absent upstream: got %q, want %q", got, want)
	}
}

func TestRunnerRejectsUnsupportedOperator(t *testing.T) {
	// http_api was removed (ADR 0047/0048, #512). A leftover spec declaring it is
	// rejected by the agent as an unsupported operator (defense in depth; the
	// control plane refuses it at registration).
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "http_api"}}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	if err := r.Run(context.Background()); err == nil {
		t.Error("agent must refuse to run a removed/unsupported operator")
	}
}

func TestRunnerPropagatesGetSpecError(t *testing.T) {
	client := &fakeClient{getSpecErr: errors.New("boom")}
	r := newRunner(client, &fakeCmd{}, &recordingSink{})
	if err := r.Run(context.Background()); err == nil {
		t.Error("GetTaskSpec failure should abort the run")
	}
}

// TestRunnerReschedule verifies a reschedule-mode sensor that signaled reschedule
// (sentinel exit + a reschedule-time file the runtime wrote) is reported as
// up_for_reschedule with reschedule_at — NOT failed — so the scheduler re-dispatches
// it later without consuming retry budget (ADR 0040 Phase B, #380).
func TestRunnerReschedule(t *testing.T) {
	rp := filepath.Join(t.TempDir(), "reschedule.txt")
	if err := os.WriteFile(rp, []byte("2099-01-02T03:04:05+00:00\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:sensor"}}
	cmd := &fakeCmd{exitCode: rescheduleExitCode} // the runtime signaled reschedule
	r := newRunner(client, cmd, &recordingSink{})
	r.ReschedulePath = rp

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: reschedule must not surface as an error: %v", err)
	}
	if len(client.reports) == 0 {
		t.Fatal("no state reported")
	}
	last := client.reports[len(client.reports)-1]
	if last.GetState() != agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE {
		t.Errorf("final state = %v, want up_for_reschedule", last.GetState())
	}
	if last.GetRescheduleAt() == nil {
		t.Fatal("reschedule_at must be set on the up_for_reschedule report")
	}
	if got := last.GetRescheduleAt().AsTime().UTC().Format(time.RFC3339); got != "2099-01-02T03:04:05Z" {
		t.Errorf("reschedule_at = %s, want 2099-01-02T03:04:05Z", got)
	}
	// The agent must also tell the runtime where to write the reschedule time.
	foundEnv := false
	for _, e := range cmd.env {
		if strings.HasPrefix(e, "LEOFLOW_RESCHEDULE_PATH=") {
			foundEnv = true
		}
	}
	if !foundEnv {
		t.Error("buildEnv must set LEOFLOW_RESCHEDULE_PATH so the runtime can signal reschedule")
	}
}

// TestRunnerExit75WithoutRescheduleFileIsNormalFailure pins #386: exit 75
// (EX_TEMPFAIL) is the reschedule sentinel, but the reschedule FILE is the real
// signal. A user task that exits 75 without writing the file is an ordinary
// failure — it must not be hijacked into the reschedule path nor reported as
// up_for_reschedule, and the error must read as a normal exit, not "reschedule".
func TestRunnerExit75WithoutRescheduleFileIsNormalFailure(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:t"}}
	cmd := &fakeCmd{exitCode: rescheduleExitCode} // exit 75, no reschedule file written
	r := newRunner(client, cmd, &recordingSink{})
	r.ReschedulePath = filepath.Join(t.TempDir(), "reschedule.txt") // path set, file absent

	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("exit 75 without a reschedule file must fail")
	}
	if strings.Contains(err.Error(), "reschedule") {
		t.Errorf("exit 75 without a file must fail as a normal exit, not a reschedule: %v", err)
	}
	for _, rep := range client.reports {
		if rep.GetState() == agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE {
			t.Error("exit 75 without a file must not report up_for_reschedule")
		}
	}
	if last := client.reports[len(client.reports)-1]; last.GetState() != agentv1.TaskState_TASK_STATE_FAILED {
		t.Errorf("final state = %v, want failed", last.GetState())
	}
}
