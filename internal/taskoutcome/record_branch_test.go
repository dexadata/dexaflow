package taskoutcome

import (
	"reflect"
	"strings"
	"testing"
)

func TestBranchRecordRoundTrips(t *testing.T) {
	rec := Succeeded().WithBranch(BranchDecision{FollowTaskIDs: []string{"a", "b"}})
	enc, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Decode(enc)
	if !ok || got.Branch == nil || !reflect.DeepEqual(got.Branch.FollowTaskIDs, []string{"a", "b"}) {
		t.Fatalf("Decode(%s) = %+v, %v", enc, got, ok)
	}
}

// A decision that follows nothing is still a decision: it must not decode as
// a record without one.
func TestBranchRecordFollowingNothingKeepsItsDecision(t *testing.T) {
	enc, err := Succeeded().WithBranch(BranchDecision{}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := Decode(enc); !ok || got.Branch == nil {
		t.Fatalf("Decode(%s) = %+v, want a decision following nothing", enc, got)
	}
}

func TestRecordWithoutBranchKeepsItsBytes(t *testing.T) {
	enc, err := Succeeded().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if enc != `{"v":1,"outcome":"success"}` {
		t.Errorf("Encode = %s", enc)
	}
}

// A decision fits up to the branch budget, under the termination message's
// 4 KiB; past it the record is refused, so the agent writes none and the
// task never settles a success without its decision (ADR 0064 D6).
func TestBranchRecordBudget(t *testing.T) {
	ids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = strings.Repeat("t", 30)
		}
		return out
	}
	if _, err := Succeeded().WithBranch(BranchDecision{FollowTaskIDs: ids(60)}).Encode(); err != nil {
		t.Errorf("a 60-id decision must fit: %v", err)
	}
	if _, err := Succeeded().WithBranch(BranchDecision{FollowTaskIDs: ids(200)}).Encode(); err == nil {
		t.Error("a decision past the budget must be refused")
	}
}
