//go:build integration

package scheduler_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

// freezeProxy forwards TCP to target until it is frozen, after which it keeps
// both sockets open and silently drops every byte: what a client sees when the
// database host vanishes behind a failover or a partition, without a reset.
type freezeProxy struct {
	ln     net.Listener
	frozen atomic.Bool
	mu     sync.Mutex
	conns  []net.Conn
}

func newFreezeProxy(t *testing.T, target string) *freezeProxy {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &freezeProxy{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, s)
			p.mu.Unlock()
			go p.pipe(c, s)
			go p.pipe(s, c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			_ = c.Close()
		}
		p.mu.Unlock()
	})
	return p
}

func (p *freezeProxy) pipe(dst, src net.Conn) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if err != nil {
			return
		}
		if p.frozen.Load() {
			continue // swallowed: the peer never answers
		}
		if _, werr := dst.Write(buf[:n]); werr != nil {
			return
		}
	}
}

// blackholedLeader returns a scheduler leader that holds the lock over a
// session routed through a freezeProxy, and the proxy.
func blackholedLeader(t *testing.T) (*scheduler.Leader, *freezeProxy) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL must point at a Postgres for integration tests")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		t.Skipf("DATABASE_URL is not a URL with a TCP host: %v", err)
	}
	p := newFreezeProxy(t, u.Host)
	u.Host = p.ln.Addr().String()
	ctx := context.Background()
	pool, err := storage.NewLeaderPool(ctx, config.DatabaseSection{URL: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	// Closing a pool whose session is frozen can block on the terminate
	// message, so it is closed in the background.
	t.Cleanup(func() { go pool.Close() })
	leader := scheduler.NewLeader(pool)
	if ok, err := leader.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() {
		// Release the lock from a healthy session if the frozen one still
		// holds it on the server, so later tests can lead.
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pg, err := storage.NewLeaderPool(rctx, config.DatabaseSection{URL: dsn})
		if err != nil {
			return
		}
		defer pg.Close()
		_, _ = pg.Exec(rctx, `SELECT pg_terminate_backend(pid) FROM pg_locks
			WHERE locktype = 'advisory' AND objsubid = 1
			  AND classid::bigint = $1 AND objid::bigint = $2 AND pid <> pg_backend_pid()`,
			scheduler.LockID>>32, scheduler.LockID&0xFFFFFFFF)
	})
	return leader, p
}

// With the leader's database session black-holed, the watchdog must count the
// hung checks as failures and step down, instead of leading on a lock it can
// no longer confirm until the kernel gives up on the socket (about 15 minutes
// with the default tcp_retries2). Regression test for #1599 (R-12).
func TestLeaderStepsDownOnBlackholedSession(t *testing.T) {
	leader, p := blackholedLeader(t)
	p.frozen.Store(true)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stepped atomic.Value
	done := make(chan struct{})
	go func() {
		defer close(done)
		scheduler.WatchLeadership(runCtx, leader, 200*time.Millisecond, cancel, slog.New(slog.NewTextHandler(io.Discard, nil)),
			func(reason string) { stepped.Store(reason) })
	}()
	select {
	case <-done:
	case <-time.After(8 * time.Second): // 40 check intervals; the contract is 3 failed checks
		cancel()
		<-done
		t.Fatal("leader never stepped down on a black-holed session")
	}
	if r, _ := stepped.Load().(string); r != "check_timeout" {
		t.Errorf("step-down reason = %q, want check_timeout", r)
	}
}

// A campaign attempt on a black-holed session returns within its deadline, so
// the campaign loop keeps retrying instead of hanging on one attempt.
func TestTryLeadershipBoundedOnBlackholedSession(t *testing.T) {
	leader, p := blackholedLeader(t)
	p.frozen.Store(true)

	start := time.Now()
	if ok, err := scheduler.TryLeadership(context.Background(), leader, 300*time.Millisecond); err == nil || ok {
		t.Fatalf("tryLeadership on a frozen session = %v, %v; want an error", ok, err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("tryLeadership took %s with a 300ms deadline", elapsed)
	}
}
