package agentrpc

import (
	"context"
	"errors"
	"slices"

	"github.com/dexadata/dexaflow/internal/xcom"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// maxXComBatchItems bounds one FetchXComBatch request.
	maxXComBatchItems = 256
	// xcomBatchBudgetBytes bounds the values one FetchXComBatch response
	// carries, under the 4 MiB default gRPC receive limit of the agent. Values
	// past it are returned as deferred for the agent to fetch one by one.
	xcomBatchBudgetBytes = 3 << 20
	// xcomBatchChunk is how many keys one backend read of a batch covers. The
	// batch is read chunk by chunk and reading stops once the budget is spent,
	// so a request holds at most the budget plus one chunk of values (16 x
	// xcom.MaxSizeBytes = 4 MiB) instead of every requested value.
	xcomBatchChunk = 16
)

// xcomBatchFetcher is an XComService that reads several keys in one round
// trip (*xcom.Service). Other implementations are read key by key.
type xcomBatchFetcher interface {
	FetchMany(ctx context.Context, keys []xcom.Key) ([]xcom.Result, error)
}

// FetchXComBatch returns several upstream values in one call. Every item is
// authorized like FetchXCom before anything is read: an upstream the task did
// not declare as an input or dependency denies the whole batch. Keys are built
// from the caller's token identity, so the batch is scoped to its own tenant,
// DAG and run exactly like FetchXCom.
func (s *Server) FetchXComBatch(ctx context.Context, req *agentv1.FetchXComBatchRequest) (*agentv1.FetchXComBatchResponse, error) {
	id, err := s.identify(ctx)
	if err != nil {
		return nil, err
	}
	if aerr := s.requireAttemptToken(id); aerr != nil {
		return nil, aerr
	}
	items := req.GetItems()
	if len(items) > maxXComBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "at most %d xcom values per batch, got %d", maxXComBatchItems, len(items))
	}
	spec, err := s.store.TaskSpec(ctx, *id)
	if err != nil {
		return nil, internalStatus("loading task spec", err, attemptAttrs(id)...)
	}
	keys := make([]xcom.Key, len(items))
	for i, it := range items {
		upstream := it.GetUpstreamTaskId()
		if !declaresUpstream(spec.XComInputMapping, upstream) && !slices.Contains(spec.DependsOn, upstream) {
			return nil, status.Errorf(codes.PermissionDenied, "task %q may not read xcom from %q (not a declared input or dependency)", id.TaskID, upstream)
		}
		keys[i] = xcomKey(*id, upstream, it.GetKey())
	}
	return s.readXComBatch(ctx, keys, attemptAttrs(id))
}

// readXComBatch reads keys chunk by chunk into a response in request order,
// deferring every value that would take the response past
// xcomBatchBudgetBytes. Once a value is deferred the budget is spent: the keys
// after the current chunk are returned deferred without being read (marked
// found, since a per-value FetchXCom is what answers whether they exist), and
// the agent fetches them one by one. The first value always fits: one value is
// at most xcom.MaxSizeBytes.
func (s *Server) readXComBatch(ctx context.Context, keys []xcom.Key, attrs []any) (*agentv1.FetchXComBatchResponse, error) {
	resp := &agentv1.FetchXComBatchResponse{Items: make([]*agentv1.FetchXComBatchItem, len(keys))}
	total, spent := 0, false
	for start := 0; start < len(keys); start += xcomBatchChunk {
		chunk := keys[start:min(start+xcomBatchChunk, len(keys))]
		if spent {
			for i, k := range chunk {
				resp.Items[start+i] = &agentv1.FetchXComBatchItem{UpstreamTaskId: k.TaskID, Key: k.Name, Found: true, Deferred: true}
			}
			continue
		}
		results, err := s.fetchXComMany(ctx, chunk)
		if err != nil {
			return nil, internalStatus("reading xcom", err, attrs...)
		}
		for i, k := range chunk {
			item := &agentv1.FetchXComBatchItem{UpstreamTaskId: k.TaskID, Key: k.Name, Found: results[i].Found}
			resp.Items[start+i] = item
			if !item.Found {
				continue
			}
			e := results[i].Entry
			if spent || (total > 0 && total+len(e.Value) > xcomBatchBudgetBytes) {
				item.Deferred, spent = true, true
				continue
			}
			total += len(e.Value)
			item.Value = e.Value
			item.ContentType = e.ContentType
			item.SizeBytes = clampInt32(e.SizeBytes)
			item.CreatedAt = timestamppb.New(e.CreatedAt)
		}
	}
	return resp, nil
}

func (s *Server) fetchXComMany(ctx context.Context, keys []xcom.Key) ([]xcom.Result, error) {
	if bf, ok := s.xcom.(xcomBatchFetcher); ok {
		return bf.FetchMany(ctx, keys)
	}
	out := make([]xcom.Result, len(keys))
	for i, k := range keys {
		e, err := s.xcom.Fetch(ctx, k)
		if errors.Is(err, xcom.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[i] = xcom.Result{Entry: e, Found: true}
	}
	return out, nil
}
