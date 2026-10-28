package agentrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// lockedBuffer is a bytes.Buffer safe for the slog handler and the test to
// share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureJSONLogs routes the default logger to a JSON buffer for the test.
func captureJSONLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// rejectionLines returns the decoded "rejected agent bearer token" records.
func rejectionLines(t *testing.T, buf *lockedBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decoding log line %q: %v", line, err)
		}
		if rec["msg"] == tokenRejectedMsg {
			out = append(out, rec)
		}
	}
	return out
}

// bearerCtx is an incoming context carrying token as its bearer.
func bearerCtx(token string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
}

type countingRejections struct {
	mu      sync.Mutex
	reasons []string
}

func (c *countingRejections) RecordAgentTokenRejection(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reasons = append(c.reasons, reason)
}

// An agent whose bearer has lapsed is refused Unauthenticated, as before, and
// the control plane now says so in its own log: the reason, the attempt the
// credential belonged to and when it expired, and never the token (#1125).
// Before, this path logged nothing and an agent_lost that followed read as a
// network fault.
func TestIdentifyLogsExpiredTokenRejection(t *testing.T) {
	logs := captureJSONLogs(t)
	mint := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	id := testIdentity()
	id.AttemptEpoch, id.HasAttemptEpoch = 2, true
	token, err := authn.IssueAgentToken(id, -time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	srv := NewServer(authn, &fakeStore{}, nil)
	srv.now = func() time.Time { return mint }
	rec := &countingRejections{}
	srv.SetTokenRejectionRecorder(rec)

	_, err = srv.Register(bearerCtx(token), &agentv1.RegisterRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Register code = %v, want Unauthenticated", status.Code(err))
	}

	lines := rejectionLines(t, logs)
	if len(lines) != 1 {
		t.Fatalf("got %d rejection lines, want 1; log:\n%s", len(lines), logs.String())
	}
	got := lines[0]
	want := map[string]any{
		"level": "WARN", "reason": "expired", "ti": "ti-1", "run": "run-1",
		"dag": "etl", "task": "extract", "try": float64(1), "epoch": float64(2),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, ok := got["expired_at"]; !ok {
		t.Error("an expired rejection must say when the credential lapsed")
	}
	assertNoTokenMaterial(t, logs.String(), token)
	if len(rec.reasons) != 1 || rec.reasons[0] != auth.AgentTokenExpired {
		t.Errorf("recorded reasons = %v, want [expired]", rec.reasons)
	}
}

// The three causes the issue separates each get their own discriminator, a
// missing bearer included, and none of them logs claims it could not verify.
func TestIdentifyLogsRejectionReasons(t *testing.T) {
	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	other := auth.NewJWTAuthenticator(nil, "other-install", time.Hour)
	forged, err := other.IssueAgentToken(testIdentity(), time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	userToken, err := auth.MintUserToken("secret", time.Hour, auth.User{ID: "u1", TenantID: "acme"})
	if err != nil {
		t.Fatalf("MintUserToken: %v", err)
	}
	cases := []struct {
		name   string
		ctx    context.Context
		token  string
		reason string
	}{
		{"bad signature", bearerCtx(forged), forged, auth.AgentTokenBadSignature},
		{"wrong audience", bearerCtx(userToken), userToken, auth.AgentTokenWrongAudience},
		{"missing", context.Background(), "", tokenMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureJSONLogs(t)
			srv := NewServer(authn, &fakeStore{}, nil)
			if _, err := srv.GetTaskSpec(tc.ctx, &agentv1.GetTaskSpecRequest{}); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
			}
			lines := rejectionLines(t, logs)
			if len(lines) != 1 {
				t.Fatalf("got %d rejection lines, want 1; log:\n%s", len(lines), logs.String())
			}
			if lines[0]["reason"] != tc.reason {
				t.Errorf("reason = %v, want %s", lines[0]["reason"], tc.reason)
			}
			if _, ok := lines[0]["ti"]; ok {
				t.Errorf("a %s rejection must not log claims it did not verify: %v", tc.reason, lines[0])
			}
			if tc.token != "" {
				assertNoTokenMaterial(t, logs.String(), tc.token)
			}
		})
	}
}

// A fleet of agents with lapsed credentials retries every few seconds, so the
// line is rate limited per reason: the first lines of a window are logged, the
// rest are counted and reported on the first line of the next window. The
// metric counts every rejection.
func TestIdentifyRejectionLogIsRateLimited(t *testing.T) {
	logs := captureJSONLogs(t)
	authn := auth.NewJWTAuthenticator(nil, "secret", time.Hour)
	token, err := authn.IssueAgentToken(testIdentity(), -time.Minute)
	if err != nil {
		t.Fatalf("IssueAgentToken: %v", err)
	}
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	srv := NewServer(authn, &fakeStore{}, nil)
	srv.now = func() time.Time { return clock }
	rec := &countingRejections{}
	srv.SetTokenRejectionRecorder(rec)

	total := rejectionLogBurst + 5
	for range total {
		_, _ = srv.Register(bearerCtx(token), &agentv1.RegisterRequest{})
	}
	if n := len(rejectionLines(t, logs)); n != rejectionLogBurst {
		t.Fatalf("logged %d lines in one window, want %d", n, rejectionLogBurst)
	}
	if len(rec.reasons) != total {
		t.Errorf("metric counted %d rejections, want %d", len(rec.reasons), total)
	}

	clock = clock.Add(rejectionLogWindow)
	_, _ = srv.Register(bearerCtx(token), &agentv1.RegisterRequest{})
	lines := rejectionLines(t, logs)
	if len(lines) != rejectionLogBurst+1 {
		t.Fatalf("logged %d lines, want %d after the window rolled", len(lines), rejectionLogBurst+1)
	}
	if got := lines[len(lines)-1]["suppressed"]; got != float64(5) {
		t.Errorf("suppressed = %v, want 5", got)
	}
}

// assertNoTokenMaterial fails when the log holds the token or any of its three
// segments.
func assertNoTokenMaterial(t *testing.T, log, token string) {
	t.Helper()
	if strings.Contains(log, token) {
		t.Fatal("the log holds the bearer token")
	}
	for _, seg := range strings.Split(token, ".") {
		if len(seg) >= 8 && strings.Contains(log, seg) {
			t.Fatalf("the log holds a segment of the bearer token: %q", seg)
		}
	}
}
