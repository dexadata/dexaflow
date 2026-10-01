package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// TestClassifyBootstrapFailureNamesTheCause locks the operator-facing half of the
// pre-registration blind spot: an agent that dies before it ever registers must
// still leave behind a reason that points at the real misconfiguration, not a
// generic "pod failed".
func TestClassifyBootstrapFailureNamesTheCause(t *testing.T) {
	cases := []struct {
		name  string
		stage BootstrapStage
		err   error
		want  string // a substring an operator would search for
	}{
		{"rejected token", StageExchange,
			status.Error(codes.Unauthenticated, "projected token is not valid"), "tokenreviews"},
		{"exchange disabled", StageExchange,
			status.Error(codes.Unimplemented, "token exchange is not enabled"), "not enabled"},
		{"insecure channel", StageExchange,
			status.Error(codes.PermissionDenied, "refusing to exchange"), "TLS"},
		{"unresolvable pod", StageExchange,
			status.Error(codes.Internal, "resolving pod"), "task attempt"},
		{"unreachable control plane", StageExchange,
			status.Error(codes.Unavailable, "connection refused"), "could not reach"},
		{"dial failure", StageDial, errors.New("tls: bad certificate"), "could not reach"},
		{"unreadable token", StageToken, errors.New("no such file"), "ServiceAccount token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyBootstrapFailure(c.stage, c.err)
			if got == "" {
				t.Fatal("every pre-registration failure must classify to a non-empty reason")
			}
			if !strings.Contains(got, c.want) {
				t.Errorf("classification = %q, want it to mention %q", got, c.want)
			}
			if len(got) > taskoutcome.MaxReasonLen {
				t.Errorf("classification is %d bytes, over the %d cap", len(got), taskoutcome.MaxReasonLen)
			}
		})
	}
}

// TestClassifyBootstrapFailureNeverEchoesTheError is the security invariant: the
// reason is an end-user-visible field, so it must be drawn from a closed set of
// classifications and never carry the raw internal error — which can name
// credentials, token paths, or internal endpoints.
func TestClassifyBootstrapFailureNeverEchoesTheError(t *testing.T) {
	const secret = "bearer-eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9-supersecret"
	stages := []BootstrapStage{StageToken, StageDial, StageExchange}
	for _, stage := range stages {
		for _, err := range []error{
			errors.New(secret),
			status.Error(codes.Unauthenticated, secret),
			status.Error(codes.Internal, secret),
		} {
			got := ClassifyBootstrapFailure(stage, err)
			if strings.Contains(got, secret) || strings.Contains(got, "supersecret") {
				t.Fatalf("classification leaked the raw error: %q", got)
			}
		}
	}
}

// TestClassifyBootstrapFailureNilIsEmpty keeps the classifier honest: no error,
// no reason (the caller must never write a failure record for a healthy start).
func TestClassifyBootstrapFailureNilIsEmpty(t *testing.T) {
	if got := ClassifyBootstrapFailure(StageExchange, nil); got != "" {
		t.Errorf("nil error classified as %q, want empty", got)
	}
}

// TestReportBootstrapFailureWritesADecodableRecord proves the reason actually
// reaches the control plane's only channel for a pod that never registered: the
// container termination message the reconciler already reads.
func TestReportBootstrapFailureWritesADecodableRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termination-log")
	ReportBootstrapFailure(path, StageExchange, status.Error(codes.Unauthenticated, "projected token is not valid"))

	raw, err := os.ReadFile(path) //nolint:gosec // G304: test-owned temp path.
	if err != nil {
		t.Fatalf("bootstrap failure must leave a termination message: %v", err)
	}
	rec, ok := taskoutcome.Decode(string(raw))
	if !ok {
		t.Fatalf("termination message is not a decodable outcome record: %q", raw)
	}
	if rec.Outcome != taskoutcome.Failed {
		t.Errorf("outcome = %q, want %q", rec.Outcome, taskoutcome.Failed)
	}
	if !strings.Contains(rec.Reason, "tokenreviews") {
		t.Errorf("record reason = %q, want the token-rejection classification", rec.Reason)
	}
}

// TestReportBootstrapFailureNoPathIsNoop keeps the agent runnable outside a pod
// (Lite/subprocess), where there is no termination message to write.
func TestReportBootstrapFailureNoPathIsNoop(t *testing.T) {
	ReportBootstrapFailure("", StageExchange, errors.New("boom")) // must not panic
}
