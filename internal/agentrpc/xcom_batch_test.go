package agentrpc

import (
	"bytes"
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// batchXCom is a fakeXCom that also serves FetchMany and counts the calls.
type batchXCom struct {
	fakeXCom
	batches int
	fetches int
}

func (x *batchXCom) Fetch(ctx context.Context, key xcom.Key) (xcom.Entry, error) {
	x.fetches++
	return x.fakeXCom.Fetch(ctx, key)
}

func (x *batchXCom) FetchMany(_ context.Context, keys []xcom.Key) ([]xcom.Result, error) {
	x.batches++
	out := make([]xcom.Result, len(keys))
	for i, k := range keys {
		out[i].Entry, out[i].Found = x.entries[k.String()]
	}
	return out, nil
}

func batchStore() *fakeStore {
	return &fakeStore{spec: TaskSpec{
		XComInputMapping: map[string][]string{"a": {"up1"}, "b": {"up2", "up3"}},
		DependsOn:        []string{"up1", "up4"},
	}}
}

func batchEntries() map[string]xcom.Entry {
	return map[string]xcom.Entry{
		"xcom:acme:etl:run-1:up1:return_value": {Value: []byte(`1`), ContentType: "application/json", SizeBytes: 1},
		"xcom:acme:etl:run-1:up2:return_value": {Value: []byte(`2`), ContentType: "application/json", SizeBytes: 1},
		"xcom:acme:etl:run-1:up4:custom":       {Value: []byte(`4`), ContentType: "application/json", SizeBytes: 1},
		// Same task ids in another run and another tenant: never visible.
		"xcom:acme:etl:run-2:up3:return_value":   {Value: []byte(`"other run"`)},
		"xcom:globex:etl:run-1:up3:return_value": {Value: []byte(`"other tenant"`)},
	}
}

func batchRequest() *agentv1.FetchXComBatchRequest {
	return &agentv1.FetchXComBatchRequest{Items: []*agentv1.FetchXComRequest{
		{UpstreamTaskId: "up1"},
		{UpstreamTaskId: "up2", Key: "return_value"},
		{UpstreamTaskId: "up3"},
		{UpstreamTaskId: "up4", Key: "custom"},
	}}
}

// TestFetchXComBatchReturnsValuesInOrder: one call returns every requested
// value in request order, absent ones as not found, and reads the backend once.
func TestFetchXComBatchReturnsValuesInOrder(t *testing.T) {
	x := &batchXCom{fakeXCom: fakeXCom{entries: batchEntries()}}
	srv, a := newServerX(batchStore(), x)
	resp, err := srv.FetchXComBatch(ctxWithToken(t, a), batchRequest())
	if err != nil {
		t.Fatalf("FetchXComBatch: %v", err)
	}
	items := resp.GetItems()
	if len(items) != 4 {
		t.Fatalf("got %d items, want 4", len(items))
	}
	want := []struct {
		task, key, value string
		found            bool
	}{
		{"up1", "return_value", `1`, true},
		{"up2", "return_value", `2`, true},
		{"up3", "return_value", ``, false},
		{"up4", "custom", `4`, true},
	}
	for i, w := range want {
		it := items[i]
		if it.GetUpstreamTaskId() != w.task || it.GetKey() != w.key || it.GetFound() != w.found ||
			string(it.GetValue()) != w.value || it.GetDeferred() {
			t.Errorf("item %d = %+v, want %+v", i, it, w)
		}
	}
	if x.batches != 1 || x.fetches != 0 {
		t.Errorf("backend reads = %d batches + %d single, want 1 + 0", x.batches, x.fetches)
	}
}

// TestFetchXComBatchWithoutBatchBackend: an XCom service without FetchMany is
// read one key at a time with the same answer.
func TestFetchXComBatchWithoutBatchBackend(t *testing.T) {
	srv, a := newServerX(batchStore(), &fakeXCom{entries: batchEntries()})
	resp, err := srv.FetchXComBatch(ctxWithToken(t, a), batchRequest())
	if err != nil {
		t.Fatalf("FetchXComBatch: %v", err)
	}
	if got := resp.GetItems(); len(got) != 4 || !got[0].GetFound() || got[2].GetFound() || string(got[3].GetValue()) != `4` {
		t.Errorf("items = %+v", got)
	}
}

// TestFetchXComBatchDeniesUndeclaredUpstream: one undeclared upstream denies
// the whole batch, exactly like FetchXCom, and returns no value.
func TestFetchXComBatchDeniesUndeclaredUpstream(t *testing.T) {
	x := &batchXCom{fakeXCom: fakeXCom{entries: batchEntries()}}
	srv, a := newServerX(batchStore(), x)
	req := batchRequest()
	req.Items = append(req.Items, &agentv1.FetchXComRequest{UpstreamTaskId: "secret"})
	resp, err := srv.FetchXComBatch(ctxWithToken(t, a), req)
	if status.Code(err) != codes.PermissionDenied || resp != nil {
		t.Errorf("FetchXComBatch with an undeclared upstream = %v, %v; want PermissionDenied", resp, err)
	}
	if x.batches+x.fetches != 0 {
		t.Error("the backend was read before authorization finished")
	}
}

func TestFetchXComBatchRejectsTooManyItems(t *testing.T) {
	srv, a := newServerX(batchStore(), &fakeXCom{})
	req := &agentv1.FetchXComBatchRequest{}
	for i := 0; i <= maxXComBatchItems; i++ {
		req.Items = append(req.Items, &agentv1.FetchXComRequest{UpstreamTaskId: "up1"})
	}
	if _, err := srv.FetchXComBatch(ctxWithToken(t, a), req); status.Code(err) != codes.InvalidArgument {
		t.Errorf("oversized batch: code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestFetchXComBatchRequiresAttemptToken(t *testing.T) {
	srv, a := newServerX(batchStore(), &fakeXCom{})
	if _, err := srv.FetchXComBatch(ctxWithWarmToken(t, a), batchRequest()); status.Code(err) != codes.PermissionDenied {
		t.Errorf("warm-worker token: code = %v, want PermissionDenied", status.Code(err))
	}
}

// TestFetchXComBatchDefersPastTheBudget: values that would push the response
// over the size budget are marked deferred (the agent fetches them one by
// one), so a batch never exceeds the client's message limit. The first value
// is always delivered.
func TestFetchXComBatchDefersPastTheBudget(t *testing.T) {
	big := bytes.Repeat([]byte("x"), xcom.MaxSizeBytes)
	entries := map[string]xcom.Entry{}
	store := &fakeStore{spec: TaskSpec{XComInputMapping: map[string][]string{}}}
	req := &agentv1.FetchXComBatchRequest{}
	for i := 0; i < 20; i++ {
		task := string(rune('a' + i))
		store.spec.XComInputMapping[task] = []string{task}
		entries["xcom:acme:etl:run-1:"+task+":return_value"] = xcom.Entry{Value: big, SizeBytes: len(big)}
		req.Items = append(req.Items, &agentv1.FetchXComRequest{UpstreamTaskId: task})
	}
	srv, a := newServerX(store, &batchXCom{fakeXCom: fakeXCom{entries: entries}})
	resp, err := srv.FetchXComBatch(ctxWithToken(t, a), req)
	if err != nil {
		t.Fatalf("FetchXComBatch: %v", err)
	}
	var total, deferred int
	for i, it := range resp.GetItems() {
		if !it.GetFound() {
			t.Errorf("item %d not found", i)
		}
		if it.GetDeferred() {
			deferred++
			if len(it.GetValue()) != 0 {
				t.Errorf("deferred item %d carries a value", i)
			}
		}
		total += len(it.GetValue())
	}
	if resp.GetItems()[0].GetDeferred() {
		t.Error("the first value was deferred")
	}
	if deferred == 0 || total > xcomBatchBudgetBytes {
		t.Errorf("delivered %d bytes with %d deferred, want at most %d bytes and some deferred", total, deferred, xcomBatchBudgetBytes)
	}
}
