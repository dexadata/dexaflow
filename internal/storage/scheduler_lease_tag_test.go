package storage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestSessionTermTagNeverMatchesWithoutATerm: a replica that never led tags
// its sessions with a value no lease term equals, so its writes are refused
// even on a database whose term is still 0 (ADR 0106).
func TestSessionTermTagNeverMatchesWithoutATerm(t *testing.T) {
	if got := sessionTermTag(0); got != "-1" {
		t.Fatalf("tag without a term = %q, want -1", got)
	}
	if got := sessionTermTag(42); got != "42" {
		t.Fatalf("tag of term 42 = %q, want 42", got)
	}
}

// TestIsStaleLeaderTerm: only the fence's own SQLSTATE is a stale term, even
// wrapped.
func TestIsStaleLeaderTerm(t *testing.T) {
	stale := &pgconn.PgError{Code: staleLeaderTermCode}
	if !IsStaleLeaderTerm(stale) || !IsStaleLeaderTerm(fmt.Errorf("writing: %w", stale)) {
		t.Fatal("the fence's refusal is not recognized")
	}
	if IsStaleLeaderTerm(&pgconn.PgError{Code: "40001"}) || IsStaleLeaderTerm(errors.New("x")) || IsStaleLeaderTerm(nil) {
		t.Fatal("another error is taken for a stale term")
	}
}

// TestFenceTracerCountsOnlyRefusals: the tracer calls onFenced for a write the
// fence refused and for nothing else, and works without a callback.
func TestFenceTracerCountsOnlyRefusals(t *testing.T) {
	n := 0
	tr := fenceTracer{onFenced: func() { n++ }}
	ctx := context.Background()
	if got := tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{}); got != ctx {
		t.Fatal("TraceQueryStart must hand the context back")
	}
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: staleLeaderTermCode}})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: errors.New("other")})
	tr.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	if n != 1 {
		t.Fatalf("onFenced called %d times, want 1", n)
	}
	fenceTracer{}.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: &pgconn.PgError{Code: staleLeaderTermCode}})
}

// TestFencedSchedulerPoolConfig: the fenced pool is dedicated whatever
// schedulerMaxConns says, 8 connections when it is unset, opened on demand,
// with every session tagged and its refusals traced.
func TestFencedSchedulerPoolConfig(t *testing.T) {
	lease := NewSchedulerLease(nil)
	pc, err := fencedSchedulerPoolConfig(config.DatabaseSection{URL: "postgres://u:p@localhost:5432/db"}, lease, nil)
	if err != nil {
		t.Fatal(err)
	}
	if pc.MaxConns != defaultFencedSchedulerConns || pc.MinConns != 0 {
		t.Fatalf("MaxConns %d MinConns %d, want %d and 0", pc.MaxConns, pc.MinConns, defaultFencedSchedulerConns)
	}
	if pc.PrepareConn == nil {
		t.Fatal("sessions are not tagged with the lease term")
	}
	if _, ok := pc.ConnConfig.Tracer.(fenceTracer); !ok {
		t.Fatalf("tracer %T, want fenceTracer", pc.ConnConfig.Tracer)
	}
	pc, err = fencedSchedulerPoolConfig(config.DatabaseSection{URL: "postgres://u:p@localhost:5432/db", SchedulerMaxConns: 3}, lease, nil)
	if err != nil || pc.MaxConns != 3 {
		t.Fatalf("schedulerMaxConns 3: MaxConns %v, err %v", pc.MaxConns, err)
	}
	if _, err := fencedSchedulerPoolConfig(config.DatabaseSection{URL: "postgres://u:p@localhost:5432/db", SchedulerMaxConns: math.MaxInt32 + 1}, lease, nil); err == nil {
		t.Fatal("a pool size past int32 was accepted")
	}
	if _, err := fencedSchedulerPoolConfig(config.DatabaseSection{URL: "::not a url"}, lease, nil); err == nil {
		t.Fatal("an unparseable url was accepted")
	}
}
