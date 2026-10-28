package agentrpc

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
)

// tokenRejectedMsg is the message of the control-plane log line written when an
// agent RPC is refused for its bearer (#1125).
const tokenRejectedMsg = "rejected agent bearer token"

// tokenMissing is the rejection reason for an RPC that carried no bearer at all,
// next to the auth.AgentToken* reasons for one that failed verification.
const tokenMissing = "missing"

// rejectionLogWindow and rejectionLogBurst rate limit the rejection log line per
// reason: at most rejectionLogBurst lines per window, the rest counted and
// reported on the first line of the next window. A fleet whose credentials
// lapsed together (a control-plane outage longer than the token TTL) retries
// every heartbeat, and a client spraying bad tokens can call as fast as it
// likes; neither may flood the log. The metric counts every rejection.
const (
	rejectionLogWindow = time.Minute
	rejectionLogBurst  = 10
)

// TokenRejectionRecorder counts agent RPCs refused for their bearer token, by
// reason (one of the auth.AgentToken* constants, or "missing").
// *observability.Metrics satisfies it.
type TokenRejectionRecorder interface {
	RecordAgentTokenRejection(reason string)
}

// rejectionRecorderBox holds a TokenRejectionRecorder behind an atomic pointer.
type rejectionRecorderBox struct{ r TokenRejectionRecorder }

// SetTokenRejectionRecorder attaches the rejection meter. A nil recorder (the
// default) counts nothing; the log line is written either way. It is safe to
// call while the server is already serving.
func (s *Server) SetTokenRejectionRecorder(r TokenRejectionRecorder) {
	s.rejectionMeter.Store(&rejectionRecorderBox{r: r})
}

// rejectionLimiter is the per-reason window behind the rejection log line. The
// reasons are a closed set, so the map stays a handful of entries.
type rejectionLimiter struct {
	mu       sync.Mutex
	byReason map[string]*rejectionWindow
}

// rejectionWindow is one reason's current window.
type rejectionWindow struct {
	start      time.Time
	logged     int
	suppressed int
	// carried is the previous window's suppressed count, reported on the first
	// line of this one.
	carried int
}

// allow reports whether a rejection for reason may be logged at now, and how
// many lines of the previous window were suppressed (reported once, on the
// first line of a new window).
func (l *rejectionLimiter) allow(reason string, now time.Time) (ok bool, suppressed int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byReason == nil {
		l.byReason = map[string]*rejectionWindow{}
	}
	w := l.byReason[reason]
	if w == nil || now.Sub(w.start) >= rejectionLogWindow || now.Before(w.start) {
		carried := 0
		if w != nil {
			carried = w.suppressed
		}
		w = &rejectionWindow{start: now, carried: carried}
		l.byReason[reason] = w
	}
	if w.logged >= rejectionLogBurst {
		w.suppressed++
		return false, 0
	}
	w.logged++
	carried := w.carried
	w.carried = 0
	return true, carried
}

// rejectToken meters and logs one refused bearer. The line carries the reason,
// the RPC, the caller's address and, only when the token's signature and
// audience verified (an expired one), the attempt it belonged to. It never
// carries the token or any part of it.
func (s *Server) rejectToken(ctx context.Context, rej *auth.AgentTokenRejection) {
	if box := s.rejectionMeter.Load(); box != nil && box.r != nil {
		box.r.RecordAgentTokenRejection(rej.Reason)
	}
	now := s.now()
	ok, suppressed := s.rejections.allow(rej.Reason, now)
	if !ok {
		return
	}
	attrs := []any{"reason", rej.Reason}
	if method, found := grpc.Method(ctx); found {
		attrs = append(attrs, "method", method)
	}
	if p, found := peer.FromContext(ctx); found && p.Addr != nil {
		attrs = append(attrs, "peer", p.Addr.String())
	}
	if id := rej.Identity; id != nil {
		attrs = append(attrs, "ti", id.TaskInstanceID, "tenant", id.TenantID, "dag", id.DagID,
			"run", id.RunID, "task", id.TaskID, "try", id.TryNumber)
		if id.HasAttemptEpoch {
			attrs = append(attrs, "epoch", id.AttemptEpoch)
		}
		if id.Scope != "" {
			attrs = append(attrs, "scope", id.Scope, "worker", id.WorkerID)
		}
	}
	if !rej.ExpiredAt.IsZero() {
		attrs = append(attrs, "expired_at", rej.ExpiredAt.UTC(), "expired_for", now.Sub(rej.ExpiredAt).Round(time.Second).String())
	}
	if suppressed > 0 {
		attrs = append(attrs, "suppressed", suppressed)
	}
	slog.Warn(tokenRejectedMsg, attrs...)
}
