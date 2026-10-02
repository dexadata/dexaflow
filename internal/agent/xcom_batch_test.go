package agent

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// batchSpec declares a single input, a fan-in with one absent upstream, and,
// as a captured operator, two depends_on upstreams (one shared with the input).
func batchSpec() *agentv1.TaskSpec {
	return &agentv1.TaskSpec{
		Operator: "airflow_operator", OperatorClass: "x.Op",
		XcomInputMapping: map[string]*agentv1.XComUpstreams{
			"single": {TaskIds: []string{"up1"}},
			"many":   {TaskIds: []string{"up2", "gone", "up3"}},
		},
		DependsOn: []string{"up1", "up4"},
	}
}

func batchClient() *fakeClient {
	return &fakeClient{xcom: map[string]*agentv1.FetchXComResponse{
		"up1": {Value: []byte(`1`)},
		"up2": {Value: []byte(`2`)},
		"up3": {Value: []byte(`3`)},
		"up4": {Value: []byte(`{"k":4}`)},
	}}
}

func xcomEnv(t *testing.T, f *fakeClient) []string {
	t.Helper()
	env, err := (&Runner{Client: f}).buildEnv(context.Background(), batchSpec())
	if err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "LEOFLOW_XCOM_") || strings.HasPrefix(kv, upstreamXComEnv+"=") {
			out = append(out, kv)
		}
	}
	sort.Strings(out)
	return out
}

// legacyEnv is what the per-value FetchXCom path delivers.
func legacyEnv(t *testing.T) []string {
	f := batchClient()
	f.batchErr = status.Error(codes.Unimplemented, "unknown method FetchXComBatch")
	return xcomEnv(t, f)
}

// TestBuildEnvFetchesXComInOneBatch: every upstream value an attempt needs
// (inputs, fan-in members and a captured operator's depends_on) arrives in one
// FetchXComBatch call, with the same environment the per-value path builds.
func TestBuildEnvFetchesXComInOneBatch(t *testing.T) {
	f := batchClient()
	got := xcomEnv(t, f)
	if f.batchCalls != 1 || f.fetchCalls != 0 {
		t.Errorf("calls = %d batch + %d single, want 1 + 0", f.batchCalls, f.fetchCalls)
	}
	want := legacyEnv(t)
	if len(want) != 3 {
		t.Fatalf("legacy env = %v, want three XCom variables", want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("batch env = %v\nwant (per-value path) %v", got, want)
	}
}

// TestBuildEnvFallsBackWithoutBatchRPC: an older control plane answers
// Unimplemented and the agent fetches value by value as before.
func TestBuildEnvFallsBackWithoutBatchRPC(t *testing.T) {
	f := batchClient()
	f.batchErr = status.Error(codes.Unimplemented, "unknown method FetchXComBatch")
	xcomEnv(t, f)
	if f.batchCalls != 1 || f.fetchCalls != 6 {
		t.Errorf("calls = %d batch + %d single, want 1 + 6 (one per use, as before)", f.batchCalls, f.fetchCalls)
	}
}

// TestBuildEnvFetchesDeferredValuesOneByOne: values the batch deferred for
// size are fetched with FetchXCom.
func TestBuildEnvFetchesDeferredValuesOneByOne(t *testing.T) {
	f := batchClient()
	f.deferred = map[string]bool{"up3": true}
	got := xcomEnv(t, f)
	if f.fetchCalls != 1 {
		t.Errorf("single fetches = %d, want 1 (the deferred value)", f.fetchCalls)
	}
	if want := legacyEnv(t); !reflect.DeepEqual(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

// TestBuildEnvBatchErrorKeepsPerValueErrors: any other batch failure falls
// back to the per-value path, so the task fails with the same error as before.
func TestBuildEnvBatchErrorKeepsPerValueErrors(t *testing.T) {
	f := batchClient()
	f.fetchXComErr = status.Error(codes.Unavailable, "control plane gone")
	_, err := (&Runner{Client: f}).buildEnv(context.Background(), batchSpec())
	if err == nil || !strings.Contains(err.Error(), "control plane gone") {
		t.Errorf("buildEnv error = %v, want the FetchXCom error", err)
	}
}

// TestBuildEnvChunksLargeFanIn: a fan-in over more upstreams than one batch
// may carry is read in several batches instead of failing the batch and
// falling back to one FetchXCom per upstream.
func TestBuildEnvChunksLargeFanIn(t *testing.T) {
	f := &fakeClient{xcom: map[string]*agentv1.FetchXComResponse{}}
	members := make([]string, 300)
	for i := range members {
		members[i] = fmt.Sprintf("up%03d", i)
		f.xcom[members[i]] = &agentv1.FetchXComResponse{Value: []byte(fmt.Sprintf("%d", i))}
	}
	spec := &agentv1.TaskSpec{Operator: "python", XcomInputMapping: map[string]*agentv1.XComUpstreams{
		"all": {TaskIds: members},
	}}
	if _, err := (&Runner{Client: f}).buildEnv(context.Background(), spec); err != nil {
		t.Fatalf("buildEnv: %v", err)
	}
	if f.batchCalls != 2 || f.fetchCalls != 0 {
		t.Errorf("calls = %d batch + %d single, want 2 + 0", f.batchCalls, f.fetchCalls)
	}
}

// BenchmarkBuildEnvXCom is the agent side of an attempt with eight upstream
// values against a fake client: rpcs/attempt counts the XCom RPCs, each a
// network round trip to the control plane in a real pod.
func BenchmarkBuildEnvXCom(b *testing.B) {
	f := &fakeClient{xcom: map[string]*agentv1.FetchXComResponse{}}
	spec := &agentv1.TaskSpec{Operator: "python", XcomInputMapping: map[string]*agentv1.XComUpstreams{}}
	for i := 0; i < 8; i++ {
		id := string(rune('a' + i))
		f.xcom[id] = &agentv1.FetchXComResponse{Value: []byte(`{"rows":1}`)}
		spec.XcomInputMapping["p"+id] = &agentv1.XComUpstreams{TaskIds: []string{id}}
	}
	r := &Runner{Client: f}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := r.buildEnv(context.Background(), spec); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(f.batchCalls+f.fetchCalls)/float64(b.N), "rpcs/attempt")
}
