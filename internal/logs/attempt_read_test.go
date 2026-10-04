package logs

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
)

// trackingStore wraps a memStore, counting every Get by key and how many
// returned readers are open at once. listing adds List, as S3 and GCS have.
type trackingStore struct {
	*memStore
	mu      sync.Mutex
	gets    map[string]int
	open    int
	maxOpen int
}

func newTrackingStore() *trackingStore {
	return &trackingStore{memStore: newMemStore(), gets: map[string]int{}}
}

func (s *trackingStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	s.gets[key]++
	s.mu.Unlock()
	rc, err := s.memStore.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.open++
	s.maxOpen = max(s.maxOpen, s.open)
	s.mu.Unlock()
	return &trackedReader{ReadCloser: rc, s: s}, nil
}

func (s *trackingStore) totalGets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.gets {
		n += c
	}
	return n
}

type trackedReader struct {
	io.ReadCloser
	s      *trackingStore
	closed bool
}

func (r *trackedReader) Close() error {
	if !r.closed {
		r.closed = true
		r.s.mu.Lock()
		r.s.open--
		r.s.mu.Unlock()
	}
	return r.ReadCloser.Close()
}

// listingStore is a trackingStore that also lists keys under a prefix.
type listingStore struct {
	*trackingStore
	lists int
}

func (s *listingStore) List(_ context.Context, prefix, delimiter string) ([]string, error) {
	s.lists++
	s.memStore.mu.Lock()
	defer s.memStore.mu.Unlock()
	seen := map[string]bool{}
	for k := range s.memStore.objs {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		if i := strings.Index(rest, delimiter); delimiter != "" && i >= 0 {
			seen[prefix+rest[:i+len(delimiter)]] = true
			continue
		}
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

// TestSegmentedLayoutKeepsEachExecutionApart: in the segmented layout every
// execution of a try writes its own segment directory, {try}.log.d for epoch 0
// and {try}.e{epoch}.log.d otherwise, so a later execution neither appends to
// nor hides an earlier one, and reading the try serves both.
func TestSegmentedLayoutKeepsEachExecutionApart(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(ObjectLayoutSegmented))
	writeStream(t, sink, epochRef(0), "zero")
	writeStream(t, sink, epochRef(1), "one")

	for _, key := range []string{"acme/etl/run-1/extract/1.log.d/00000000.log", "acme/etl/run-1/extract/1.e1.log.d/00000000.log"} {
		if _, ok := store.objs[key]; !ok {
			t.Errorf("segment %s missing; stored: %v", key, keys(store))
		}
	}
	rc, err := ReadAttempts(sink, ref(), TryEpochs{Low: 0, High: 1})
	if err != nil {
		t.Fatalf("ReadAttempts: %v", err)
	}
	got := streamMessages(readBody(t, rc))
	if len(got) != 4 || got[1][1] != "zero" || got[3][1] != "one" {
		t.Fatalf("want system, zero, system, one; got %v", got)
	}
}

func keys(m *memStore) []string {
	out := make([]string, 0, len(m.objs))
	for k := range m.objs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestReadAttemptsListsInsteadOfProbing: a store that can list answers which
// executions of the try are stored in one call, so a try spanning many epochs
// fetches only the streams that exist instead of probing every epoch (and
// segment zero of each) for a missing key.
func TestReadAttemptsListsInsteadOfProbing(t *testing.T) {
	for _, layout := range []string{ObjectLayoutSingle, ObjectLayoutSegmented} {
		t.Run(layout, func(t *testing.T) {
			store := &listingStore{trackingStore: newTrackingStore()}
			sink := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(layout))
			writeStream(t, sink, epochRef(3), "three")
			writeStream(t, sink, epochRef(150), "one-fifty")
			before := store.totalGets()

			rc, err := ReadAttempts(sink, ref(), TryEpochs{Low: 0, High: 200})
			if err != nil {
				t.Fatalf("ReadAttempts: %v", err)
			}
			got := streamMessages(readBody(t, rc))
			if len(got) != 4 || got[1][1] != "three" || got[3][1] != "one-fifty" {
				t.Fatalf("want both executions; got %v", got)
			}
			if store.lists != 1 {
				t.Errorf("lists = %d, want 1", store.lists)
			}
			// Each stored execution costs at most a stream fetch and, for a
			// segmented one, the probe of the next segment and of the marker
			// object: never a probe per unused epoch.
			if n := store.totalGets() - before; n > 6 {
				t.Errorf("%d fetches for 2 stored executions over 201 epochs: %v", n, store.gets)
			}
		})
	}
}

// TestReadAttemptsOpensStreamsLazily: a try with many executions holds one
// stream open at a time while it is read, not one per execution.
func TestReadAttemptsOpensStreamsLazily(t *testing.T) {
	for name, store := range map[string]ObjectStore{
		"listing":   &listingStore{trackingStore: newTrackingStore()},
		"unlisting": newTrackingStore(),
	} {
		t.Run(name, func(t *testing.T) {
			sink := NewObjectSink(context.Background(), store, "", nil)
			for e := 1; e <= 5; e++ {
				writeStream(t, sink, epochRef(e), "line")
			}
			rc, err := ReadAttempts(sink, ref(), TryEpochs{Low: 0, High: 5})
			if err != nil {
				t.Fatalf("ReadAttempts: %v", err)
			}
			if got := len(streamMessages(readBody(t, rc))); got != 10 {
				t.Fatalf("want 5 system and 5 task lines, got %d", got)
			}
			ts := trackerOf(store)
			// Without a listing the reader looks one stream ahead to know
			// whether the try has more than one execution.
			if want := map[string]int{"listing": 1, "unlisting": 2}[name]; ts.maxOpen > want {
				t.Errorf("%d streams open at once, want at most %d", ts.maxOpen, want)
			}
			if ts.open != 0 {
				t.Errorf("%d streams left open after Close", ts.open)
			}
		})
	}
}

func trackerOf(s ObjectStore) *trackingStore {
	if l, ok := s.(*listingStore); ok {
		return l.trackingStore
	}
	return s.(*trackingStore)
}

// TestReadAttemptsNotesDroppedExecutions: when the bound on executions per
// read leaves older ones out, the read says so in a system line instead of
// silently starting mid-history.
func TestReadAttemptsNotesDroppedExecutions(t *testing.T) {
	for name, sink := range map[string]Sink{
		"listing":   NewObjectSink(context.Background(), &listingStore{trackingStore: newTrackingStore()}, "", nil),
		"unlisting": NewObjectSink(context.Background(), newTrackingStore(), "", nil),
		"disk":      NewDiskSink(t.TempDir()),
	} {
		t.Run(name, func(t *testing.T) {
			for e := 1; e <= 4; e++ {
				writeStream(t, sink, epochRef(e), "line")
			}
			rc, err := ReadAttempts(sink, ref(), TryEpochs{Low: 0, High: 4, Max: 2})
			if err != nil {
				t.Fatalf("ReadAttempts: %v", err)
			}
			got := streamMessages(readBody(t, rc))
			if len(got) != 5 {
				t.Fatalf("want a note and two executions, got %v", got)
			}
			if got[0][0] != "system" || !strings.Contains(got[0][1], "not shown") || !strings.Contains(got[0][1], "epochs 1 to 2") {
				t.Errorf("first line must note the dropped executions, got %q", got[0][1])
			}
			if !strings.Contains(got[1][1], "epoch 3") || !strings.Contains(got[3][1], "epoch 4") {
				t.Errorf("the most recent executions must be served: %v", got)
			}
		})
	}
}

// TestReadAttemptsNoNoteWithinTheBound: a try within the bound reads without
// the note, and a lone stream stays byte for byte unchanged.
func TestReadAttemptsNoNoteWithinTheBound(t *testing.T) {
	sink := NewDiskSink(t.TempDir())
	writeStream(t, sink, epochRef(2), "only")
	rc, err := ReadAttempts(sink, ref(), TryEpochs{Low: 0, High: 2, Max: 2})
	if err != nil {
		t.Fatalf("ReadAttempts: %v", err)
	}
	got := streamMessages(readBody(t, rc))
	if len(got) != 1 || got[0][1] != "only" {
		t.Fatalf("a lone stream must be served unchanged, got %v", got)
	}
}

// TestTryEpochsCandidates pins the epochs a try's log read probes when the
// sink cannot list, including a try the database has no row for: an API
// client can name a try above the task's current one while earlier tries
// archived non-zero epochs, so the upper bound is below the lower one. That
// must read as epoch 0 alone (a 404), not panic.
func TestTryEpochsCandidates(t *testing.T) {
	cases := []struct {
		name string
		set  TryEpochs
		want []int
	}{
		{"legacy try", TryEpochs{}, []int{0}},
		{"one post-upgrade try", TryEpochs{High: 3}, []int{0, 1, 2, 3}},
		{"later try", TryEpochs{Low: 3, High: 5}, []int{0, 4, 5}},
		{"unknown try above earlier archived epochs", TryEpochs{Low: 7}, []int{0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped := tc.set.candidates()
			if fmt.Sprint(got) != fmt.Sprint(tc.want) || dropped != (epochSpan{}) {
				t.Fatalf("candidates(%+v) = %v %+v, want %v", tc.set, got, dropped, tc.want)
			}
		})
	}
	got, dropped := TryEpochs{High: 1000, Max: 256}.candidates()
	if len(got) != 257 || got[0] != 0 || got[1] != 745 || dropped != (epochSpan{from: 1, to: 744}) {
		t.Fatalf("a capped range keeps epoch 0 and the latest 256 epochs, got len %d starting %v, dropped %+v", len(got), got[:2], dropped)
	}
}
