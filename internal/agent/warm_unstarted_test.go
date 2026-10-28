package agent

import (
	"context"
	"path/filepath"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// unstartedWarmFake is a warmFake whose first GetTaskSpec can fail with an error
// the runner does not retry (a transient one would be, see retryableTransientErr),
// and which records the attempt token live at each ReportState, so a test can
// tell which assignment a report was for.
type unstartedWarmFake struct {
	*warmFake
	failFirstSpec  bool
	specCalls      int
	tokenAtReport  []string
	statesByReport []agentv1.TaskState
}

func (c *unstartedWarmFake) GetTaskSpec(ctx context.Context, in *agentv1.GetTaskSpecRequest, o ...grpc.CallOption) (*agentv1.TaskSpec, error) {
	c.specCalls++
	if c.failFirstSpec && c.specCalls == 1 {
		return nil, status.Error(codes.FailedPrecondition, "attempt has no spec")
	}
	return c.warmFake.GetTaskSpec(ctx, in, o...)
}

func (c *unstartedWarmFake) ReportState(ctx context.Context, in *agentv1.ReportStateRequest, o ...grpc.CallOption) (*agentv1.ReportStateResponse, error) {
	c.tokenAtReport = append(c.tokenAtReport, c.tokens.Token())
	c.statesByReport = append(c.statesByReport, in.GetState())
	return c.fakeClient.ReportState(ctx, in, o...)
}

// reportsFor returns the states reported while token was the live attempt token.
func (c *unstartedWarmFake) reportsFor(token string) []agentv1.TaskState {
	var out []agentv1.TaskState
	for i, tok := range c.tokenAtReport {
		if tok == token {
			out = append(out, c.statesByReport[i])
		}
	}
	return out
}

func runWarmAssignments(t *testing.T, client *unstartedWarmFake, stream *fakeAssignmentStream, tokens *TokenSource, cmd *fakeCmd) {
	t.Helper()
	w := &WarmRunner{
		StreamClient: client, WorkClient: client, AttemptTokens: tokens, Cmd: cmd,
		Hostname: "w", Version: "t", ScratchDir: filepath.Join(t.TempDir(), "scratch"),
	}
	if err := w.Run(context.Background(), "dv"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
}

func twoAssignmentStream() *fakeAssignmentStream {
	return &fakeAssignmentStream{ctx: context.Background(), assignments: []*agentv1.WorkAssignment{
		{AssignmentId: "asg-1", AttemptToken: "tok-1", DagRunId: "run-a", TaskId: "t-a", TryNumber: 1},
		{AssignmentId: "asg-2", AttemptToken: "tok-2", DagRunId: "run-b", TaskId: "t-b", TryNumber: 1},
	}}
}

func assertStates(t *testing.T, what string, got []agentv1.TaskState, want ...agentv1.TaskState) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: reported %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: reported %v, want %v", what, got, want)
		}
	}
}

// TestWarmAttemptWithSpecErrorIsReportedFailed: a warm worker acks an attempt
// (the control plane binds it, state queued) and then cannot fetch its spec. The
// worker must settle that attempt with a FAILED report under its own attempt
// token before it frees the slot, as a dedicated pod's exit would; reporting
// nothing leaves the row queued and the worker counted busy for the rest of the
// pod's life (#1603).
func TestWarmAttemptWithSpecErrorIsReportedFailed(t *testing.T) {
	stream := twoAssignmentStream()
	tokens := NewTokenSource("bootstrap")
	client := &unstartedWarmFake{
		warmFake: &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: []*agentv1.TaskSpec{
			{Operator: "bash", Entrypoint: "true", RunId: "run-b", TaskId: "t-b", TryNumber: 1},
		}},
		failFirstSpec: true,
	}
	runWarmAssignments(t, client, stream, tokens, &fakeCmd{})

	assertStates(t, "attempt whose spec fetch failed", client.reportsFor("tok-1"), agentv1.TaskState_TASK_STATE_FAILED)
	assertStates(t, "next attempt", client.reportsFor("tok-2"),
		agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_SUCCESS)
	assertAckThenSlotFree(t, stream)
}

// TestWarmAttemptWithUnbuildableCommandIsReportedFailed covers the other
// failure before any report: a spec whose operator has no command.
func TestWarmAttemptWithUnbuildableCommandIsReportedFailed(t *testing.T) {
	stream := twoAssignmentStream()
	tokens := NewTokenSource("bootstrap")
	client := &unstartedWarmFake{
		warmFake: &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: []*agentv1.TaskSpec{
			{Operator: "no-such-operator", Entrypoint: "x", RunId: "run-a", TaskId: "t-a", TryNumber: 1},
			{Operator: "bash", Entrypoint: "true", RunId: "run-b", TaskId: "t-b", TryNumber: 1},
		}},
	}
	runWarmAssignments(t, client, stream, tokens, &fakeCmd{})

	assertStates(t, "attempt with an unbuildable command", client.reportsFor("tok-1"), agentv1.TaskState_TASK_STATE_FAILED)
	assertStates(t, "next attempt", client.reportsFor("tok-2"),
		agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_SUCCESS)
	assertAckThenSlotFree(t, stream)
}

// TestWarmAttemptThatReportedIsNotReportedAgain: an attempt whose own reports
// landed (here, a user failure) gets no extra FAILED from the worker.
func TestWarmAttemptThatReportedIsNotReportedAgain(t *testing.T) {
	stream := twoAssignmentStream()
	tokens := NewTokenSource("bootstrap")
	client := &unstartedWarmFake{
		warmFake: &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: []*agentv1.TaskSpec{
			{Operator: "bash", Entrypoint: "false", RunId: "run-a", TaskId: "t-a", TryNumber: 1},
			{Operator: "bash", Entrypoint: "false", RunId: "run-b", TaskId: "t-b", TryNumber: 1},
		}},
	}
	runWarmAssignments(t, client, stream, tokens, &fakeCmd{exitCode: 3})

	assertStates(t, "attempt that reported its own failure", client.reportsFor("tok-1"),
		agentv1.TaskState_TASK_STATE_RUNNING, agentv1.TaskState_TASK_STATE_FAILED)
}

// assertAckThenSlotFree checks the up-stream protocol: register, then an ack and
// a slot-free for each of the two assignments.
func assertAckThenSlotFree(t *testing.T, stream *fakeAssignmentStream) {
	t.Helper()
	var kinds []string
	for _, m := range stream.sent {
		switch {
		case m.GetRegister() != nil:
			kinds = append(kinds, "register")
		case m.GetAck() != nil:
			kinds = append(kinds, "ack:"+m.GetAck().GetAssignmentId())
		case m.GetSlotFree() != nil:
			kinds = append(kinds, "slotfree")
		}
	}
	want := []string{"register", "ack:asg-1", "slotfree", "ack:asg-2", "slotfree"}
	if len(kinds) != len(want) {
		t.Fatalf("stream messages = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("stream messages = %v, want %v", kinds, want)
		}
	}
}
