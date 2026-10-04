package executor

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/logs"
)

// refRecordingSink records the Ref of every appended marker.
type refRecordingSink struct{ refs []logs.Ref }

func (s *refRecordingSink) AppendEvent(ref logs.Ref, _ logs.Event) error {
	s.refs = append(s.refs, ref)
	return nil
}

// TestAgentLostMarkerLandsOnTheReapedExecutionsLog: the "killed: agent_lost"
// marker is appended to the log of the execution that was reaped, keyed by its
// attempt epoch, not to whichever execution of the try wrote last (#863).
func TestAgentLostMarkerLandsOnTheReapedExecutionsLog(t *testing.T) {
	store := &fakeHeartbeatStore{candidates: []AgentLostCandidate{{
		TaskInstanceID: "ti", TenantID: "tnt", DagRunID: "run", DagID: "dag", TaskID: "task",
		TryNumber: 2, AttemptEpoch: 6, LastHeartbeat: time.Now().Add(-time.Hour),
	}}}
	sink := &refRecordingSink{}
	r := newAgentLostReaper(store, reapTestLogger(), 90*time.Second, nil)
	r.sink = sink
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sink.refs) != 1 || sink.refs[0].TryNumber != 2 || sink.refs[0].AttemptEpoch != 6 {
		t.Fatalf("marker appended to %+v, want try 2 epoch 6", sink.refs)
	}
}
