package agentrpc

import (
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/metadata"
)

// refSink records the Ref every writer was opened for.
type refSink struct {
	captureSink
	refs []logs.Ref
}

func (r *refSink) Open(ref logs.Ref) (logs.LogWriter, error) {
	r.refs = append(r.refs, ref)
	return &r.captureSink, nil
}

// TestStreamLogsWritesToTheAttemptEpochKey: an execution's log stream is
// stored under its own attempt epoch, so a second execution of one try no
// longer overwrites the first (ADR 0051 amendment, #863). A token without the
// claim writes the epoch-0 key, which is where every pre-upgrade log lives.
func TestStreamLogsWritesToTheAttemptEpochKey(t *testing.T) {
	cases := map[string]struct {
		id   auth.AgentIdentity
		want int
	}{
		"claim":  {id: func() auth.AgentIdentity { i := testIdentity(); i.AttemptEpoch, i.HasAttemptEpoch = 4, true; return i }(), want: 4},
		"legacy": {id: testIdentity(), want: 0},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv, a := newServer(&fakeStore{})
			sink := &refSink{}
			srv.SetLogSink(sink)
			token, err := a.IssueAgentToken(tc.id, time.Hour)
			if err != nil {
				t.Fatalf("IssueAgentToken: %v", err)
			}
			stream := &fakeStreamLogsServer{
				ctx:  metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token)),
				msgs: []*agentv1.LogLine{{Message: "hello"}},
			}
			if err := srv.StreamLogs(stream); err != nil {
				t.Fatalf("StreamLogs: %v", err)
			}
			if len(sink.refs) != 1 || sink.refs[0].AttemptEpoch != tc.want || sink.refs[0].TryNumber != 1 {
				t.Fatalf("log opened for %+v, want try 1 epoch %d", sink.refs, tc.want)
			}
		})
	}
}
