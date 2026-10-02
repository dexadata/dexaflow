package agent

import (
	"context"
	"log/slog"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxXComBatchItems is the most items the control plane accepts in one
// FetchXComBatch (agentrpc's bound of the same name); a larger fan-in is read
// in several batches.
const maxXComBatchItems = 256

// xcomFetcher is the one call the environment build makes to read an upstream
// value. The AgentServiceClient satisfies it, and so does prefetchedXCom.
type xcomFetcher interface {
	FetchXCom(ctx context.Context, in *agentv1.FetchXComRequest, opts ...grpc.CallOption) (*agentv1.FetchXComResponse, error)
}

// prefetchedXCom answers FetchXCom for upstream return values read up front
// by one FetchXComBatch, and passes anything else (a value the batch deferred
// for size, or every value when the batch failed) to the client, so each call
// site sees exactly what a direct FetchXCom would return.
type prefetchedXCom struct {
	client xcomFetcher
	values map[string]*agentv1.FetchXComResponse
	absent map[string]bool
}

// FetchXCom serves a prefetched return value, or asks the client.
func (p *prefetchedXCom) FetchXCom(ctx context.Context, in *agentv1.FetchXComRequest, opts ...grpc.CallOption) (*agentv1.FetchXComResponse, error) {
	if key := in.GetKey(); key == "" || key == "return_value" {
		if resp, ok := p.values[in.GetUpstreamTaskId()]; ok {
			return resp, nil
		}
		if p.absent[in.GetUpstreamTaskId()] {
			return nil, status.Error(codes.NotFound, "no xcom for task "+in.GetUpstreamTaskId())
		}
	}
	return p.client.FetchXCom(ctx, in, opts...)
}

// prefetchXCom reads, in one FetchXComBatch, the return value of every
// upstream the environment build will ask for: each xcom input (fan-in members
// included) and, for a captured operator, each depends_on task. With fewer
// than two distinct upstreams it skips the batch, which would save nothing.
// More than maxXComBatchItems upstreams are read in several batches. A batch
// error leaves that batch's values to a per-value FetchXCom, as before:
// Unimplemented (an older control plane) ends batching quietly, and any other
// error is logged once at Warn and ends batching too.
func (r *Runner) prefetchXCom(ctx context.Context, spec *agentv1.TaskSpec) xcomFetcher {
	upstreams := neededUpstreams(spec)
	if len(upstreams) < 2 {
		return r.Client
	}
	p := &prefetchedXCom{client: r.Client, values: map[string]*agentv1.FetchXComResponse{}, absent: map[string]bool{}}
	for start := 0; start < len(upstreams); start += maxXComBatchItems {
		chunk := upstreams[start:min(start+maxXComBatchItems, len(upstreams))]
		req := &agentv1.FetchXComBatchRequest{Items: make([]*agentv1.FetchXComRequest, len(chunk))}
		for i, u := range chunk {
			req.Items[i] = &agentv1.FetchXComRequest{UpstreamTaskId: u, Key: "return_value"}
		}
		resp, err := r.Client.FetchXComBatch(ctx, req)
		if err != nil {
			if status.Code(err) != codes.Unimplemented {
				slog.Warn("batched xcom fetch failed; fetching the remaining values one by one",
					"task", spec.GetTaskId(), "upstreams", len(upstreams)-start, "error", err)
			}
			break
		}
		p.add(resp)
	}
	if len(p.values) == 0 && len(p.absent) == 0 {
		return r.Client
	}
	return p
}

// add records the values one FetchXComBatch delivered; a deferred value is
// left to a per-value FetchXCom.
func (p *prefetchedXCom) add(resp *agentv1.FetchXComBatchResponse) {
	for _, it := range resp.GetItems() {
		switch {
		case it.GetKey() != "return_value" || it.GetDeferred():
		case !it.GetFound():
			p.absent[it.GetUpstreamTaskId()] = true
		default:
			p.values[it.GetUpstreamTaskId()] = &agentv1.FetchXComResponse{
				Value: it.GetValue(), ContentType: it.GetContentType(),
				SizeBytes: it.GetSizeBytes(), CreatedAt: it.GetCreatedAt(),
			}
		}
	}
}

// neededUpstreams lists, once each, the upstream task ids whose return value
// buildEnv reads.
func neededUpstreams(spec *agentv1.TaskSpec) []string {
	seen := map[string]bool{}
	var out []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, upstreams := range spec.GetXcomInputMapping() {
		for _, id := range upstreams.GetTaskIds() {
			add(id)
		}
	}
	if spec.GetOperator() == "airflow_operator" {
		for _, id := range spec.GetDependsOn() {
			add(id)
		}
	}
	return out
}
