package storage

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// TestExecutionStoreOnPoolKeepsObservers: moving the agent handlers to their
// own pool (#1624) swaps the queries and nothing else, so the queue wait
// histogram (#2300) and the shared spec cache follow the store.
func TestExecutionStoreOnPoolKeepsObservers(t *testing.T) {
	var seen float64
	src := &ExecutionStore{q: queries.New(nil), specs: newSpecCache()}
	src.SetQueueWaitObserver(func(_ string, s float64) { seen = s })
	other := &Postgres{Queries: queries.New(nil)}

	got := src.OnPool(other)
	if got == src {
		t.Fatal("OnPool must return a copy")
	}
	if got.q != other.Queries {
		t.Error("OnPool must run queries on the given pool")
	}
	if got.specs != src.specs {
		t.Error("OnPool must keep the shared spec cache")
	}
	if got.queueWait == nil {
		t.Fatal("OnPool dropped the queue wait observer")
	}
	got.queueWait("t", 3)
	if seen != 3 {
		t.Errorf("observer saw %v, want 3", seen)
	}
	if src.q == other.Queries {
		t.Error("OnPool must not change the source store")
	}
}
