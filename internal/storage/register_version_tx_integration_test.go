//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// registerTxSpec builds a spec whose dags-row metadata (schedule, catchup,
// start_date, max_active_runs, description, owner, tags) is derived from tag,
// so two specs built with different tags differ on every one of those columns.
func registerTxSpec(dagID, version, tag string, maxRuns int, catchup bool) domain.DAGSpec {
	schedule := "@daily"
	if tag != "a" {
		schedule = "*/" + fmt.Sprint(10+len(tag)) + " * * * *"
	}
	return domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: version, Image: "img:" + version,
		Description: "desc " + tag, Owner: "owner_" + tag, Tags: []string{"t_" + tag},
		Schedule: &schedule, ScheduleTZ: "UTC",
		StartDate:     "2026-01-0" + fmt.Sprint(1+len(tag)%8) + "T00:00:00Z",
		MaxActiveRuns: maxRuns, Catchup: catchup,
		Tasks: []domain.TaskSpec{{TaskID: "task_" + tag, Type: domain.TaskTypePython}},
	}
}

// registerTx registers spec and returns created, failing the test on error.
func registerTx(t *testing.T, ctx context.Context, repo *storage.Repository, spec domain.DAGSpec) bool {
	t.Helper()
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	created, err := repo.RegisterDagVersion(ctx, "default", spec, hash)
	if err != nil {
		t.Fatalf("register %s@%s: %v", spec.DagID, spec.DagVersion, err)
	}
	return created
}

// dagMetadata is the part of the dags row that RegisterDagVersion writes.
type dagMetadata struct {
	Description, Owner, ScheduleTZ, CurrentVersion string
	Tags                                           []string
	Schedule                                       string
	StartDate                                      time.Time
	MaxActiveRuns                                  int
	Catchup                                        bool
}

func readDagMetadata(t *testing.T, ctx context.Context, repo *storage.Repository, dagID string) dagMetadata {
	t.Helper()
	d, err := repo.GetDag(ctx, "default", dagID)
	if err != nil {
		t.Fatalf("get dag %s: %v", dagID, err)
	}
	m := dagMetadata{
		Description: d.Description, Owner: d.Owner, ScheduleTZ: d.ScheduleTZ,
		CurrentVersion: d.CurrentVersion, Tags: d.Tags,
		MaxActiveRuns: d.MaxActiveRuns, Catchup: d.Catchup,
	}
	if d.Schedule != nil {
		m.Schedule = *d.Schedule
	}
	if d.StartDate != nil {
		m.StartDate = d.StartDate.UTC()
	}
	return m
}

// specMetadata is the dags-row metadata a spec should leave once it is current.
func specMetadata(t *testing.T, spec domain.DAGSpec) dagMetadata {
	t.Helper()
	start, err := time.Parse(time.RFC3339, spec.StartDate)
	if err != nil {
		t.Fatal(err)
	}
	return dagMetadata{
		Description: spec.Description, Owner: spec.Owner, ScheduleTZ: spec.ScheduleTZ,
		CurrentVersion: spec.DagVersion, Tags: spec.Tags, Schedule: *spec.Schedule,
		StartDate: start.UTC(), MaxActiveRuns: spec.MaxActiveRuns, Catchup: spec.Catchup,
	}
}

// TestRegisterDagVersionConflictLeavesDagUntouched: a register refused with 409
// (same version label, different content) must not apply the refused spec's
// schedule or any other metadata to the dags row (#1702).
func TestRegisterDagVersionConflictLeavesDagUntouched(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("reg_tx_conflict_%d", time.Now().UnixNano())

	first := registerTxSpec(dagID, "v1", "a", 3, false)
	if !registerTx(t, ctx, repo, first) {
		t.Fatal("first register: want created=true")
	}
	before := readDagMetadata(t, ctx, repo, dagID)

	refused := registerTxSpec(dagID, "v1", "bb", 7, true)
	hash, err := refused.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := repo.RegisterDagVersion(ctx, "default", refused, hash); !errors.Is(rerr, domain.ErrConflict) {
		t.Fatalf("want domain.ErrConflict, got %v", rerr)
	}

	if after := readDagMetadata(t, ctx, repo, dagID); !reflect.DeepEqual(after, before) {
		t.Fatalf("dags row changed by a refused register:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestRegisterDagVersionOlderRepushLeavesDagUntouched: re-pushing an older,
// already registered spec while a newer version is current is a no-op. The
// newer version stays current and its schedule stays live (#1702).
func TestRegisterDagVersionOlderRepushLeavesDagUntouched(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("reg_tx_repush_%d", time.Now().UnixNano())

	older := registerTxSpec(dagID, "v1", "a", 3, false)
	newer := registerTxSpec(dagID, "v2", "bb", 7, true)
	registerTx(t, ctx, repo, older)
	if !registerTx(t, ctx, repo, newer) {
		t.Fatal("newer register: want created=true")
	}
	before := readDagMetadata(t, ctx, repo, dagID)
	if want := specMetadata(t, newer); !reflect.DeepEqual(before, want) {
		t.Fatalf("after the newer register: got %+v want %+v", before, want)
	}

	if registerTx(t, ctx, repo, older) {
		t.Fatal("re-push of an already registered spec: want created=false")
	}

	if after := readDagMetadata(t, ctx, repo, dagID); !reflect.DeepEqual(after, before) {
		t.Fatalf("dags row changed by an identical-hash re-push:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestRegisterDagVersionConcurrentRegistersStayConsistent: concurrent registers
// of different specs for one DAG are serialized, so whichever version ends up
// current, the dags metadata is that version's spec (#1702).
func TestRegisterDagVersionConcurrentRegistersStayConsistent(t *testing.T) {
	repo, _, ctx := openRepo(t)
	for round := 0; round < 10; round++ {
		dagID := fmt.Sprintf("reg_tx_concurrent_%d_%d", round, time.Now().UnixNano())
		specs := []domain.DAGSpec{
			registerTxSpec(dagID, "v1", "a", 3, false),
			registerTxSpec(dagID, "v2", "bb", 7, true),
			registerTxSpec(dagID, "v3", "ccc", 11, false),
			registerTxSpec(dagID, "v4", "dddd", 13, true),
		}
		var wg sync.WaitGroup
		errs := make([]error, len(specs))
		start := make(chan struct{})
		for i, spec := range specs {
			hash, err := spec.CanonicalHash()
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func(i int, spec domain.DAGSpec, hash string) {
				defer wg.Done()
				<-start
				_, errs[i] = repo.RegisterDagVersion(ctx, "default", spec, hash)
			}(i, spec, hash)
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d: register %d: %v", round, i, err)
			}
		}

		got := readDagMetadata(t, ctx, repo, dagID)
		var current *domain.DAGSpec
		for i := range specs {
			if specs[i].DagVersion == got.CurrentVersion {
				current = &specs[i]
			}
		}
		if current == nil {
			t.Fatalf("round %d: current version %q is none of the registered specs", round, got.CurrentVersion)
		}
		if want := specMetadata(t, *current); !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: dags metadata does not match current version %s:\ngot  %+v\nwant %+v",
				round, current.DagVersion, got, want)
		}
		spec, err := repo.GetCurrentSpec(ctx, "default", dagID)
		if err != nil {
			t.Fatalf("round %d: current spec: %v", round, err)
		}
		if spec.DagVersion != current.DagVersion {
			t.Fatalf("round %d: current spec %s, dags row says %s", round, spec.DagVersion, current.DagVersion)
		}
	}
}

// TestRegisterDagVersionConcurrentIdenticalRegisters: the same spec pushed
// concurrently (two CI jobs of one commit) registers once. The row lock makes
// every later register see the committed version and answer created=false,
// instead of colliding on the version label and answering 409 (#1702).
func TestRegisterDagVersionConcurrentIdenticalRegisters(t *testing.T) {
	repo, _, ctx := openRepo(t)
	for round := 0; round < 10; round++ {
		dagID := fmt.Sprintf("reg_tx_same_%d_%d", round, time.Now().UnixNano())
		first := registerTxSpec(dagID, "v1", "a", 3, false)
		registerTx(t, ctx, repo, first)
		spec := registerTxSpec(dagID, "v2", "bb", 7, true)
		hash, err := spec.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		const pushers = 4
		var wg sync.WaitGroup
		created := make([]bool, pushers)
		errs := make([]error, pushers)
		start := make(chan struct{})
		for i := 0; i < pushers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				created[i], errs[i] = repo.RegisterDagVersion(ctx, "default", spec, hash)
			}(i)
		}
		close(start)
		wg.Wait()
		n := 0
		for i := range created {
			if errs[i] != nil {
				t.Fatalf("round %d: pusher %d: %v", round, i, errs[i])
			}
			if created[i] {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("round %d: %d pushers created the version, want exactly 1", round, n)
		}
		if got, want := readDagMetadata(t, ctx, repo, dagID), specMetadata(t, spec); !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: got %+v want %+v", round, got, want)
		}
	}
}

// TestRegisterDagVersionConcurrentFirstRegisters: the same first version of a
// DAG that does not exist yet, pushed concurrently, creates the DAG once and
// registers once; the other pushes answer created=false without error (#1702).
func TestRegisterDagVersionConcurrentFirstRegisters(t *testing.T) {
	repo, _, ctx := openRepo(t)
	for round := 0; round < 10; round++ {
		dagID := fmt.Sprintf("reg_tx_first_%d_%d", round, time.Now().UnixNano())
		spec := registerTxSpec(dagID, "v1", "a", 3, false)
		hash, err := spec.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		const pushers = 4
		var wg sync.WaitGroup
		created := make([]bool, pushers)
		errs := make([]error, pushers)
		start := make(chan struct{})
		for i := 0; i < pushers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				created[i], errs[i] = repo.RegisterDagVersion(ctx, "default", spec, hash)
			}(i)
		}
		close(start)
		wg.Wait()
		n := 0
		for i := range created {
			if errs[i] != nil {
				t.Fatalf("round %d: pusher %d: %v", round, i, errs[i])
			}
			if created[i] {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("round %d: %d pushers created the version, want exactly 1", round, n)
		}
		if got, want := readDagMetadata(t, ctx, repo, dagID), specMetadata(t, spec); !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: got %+v want %+v", round, got, want)
		}
	}
}
