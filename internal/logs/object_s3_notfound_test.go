package logs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// s3StoreAnswering builds an S3Store against a server that answers every
// request with status and body, so the test drives the SDK's own error
// decoding instead of a hand-built error value.
func s3StoreAnswering(t *testing.T, status int, body string) *S3Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body)) //nolint:errcheck // test server
	}))
	t.Cleanup(srv.Close)
	store, err := NewS3Store(context.Background(), S3Config{
		Bucket: "task-logs", Region: "us-east-1", Endpoint: srv.URL, ForcePathStyle: true,
		AccessKeyID: "test", SecretAccessKey: "test-secret",
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	return store
}

// TestS3StoreGetMatchesNotFoundBeyondTheModeledError pins #647: some
// S3-compatible stores answer a missing key with a 404 that is not the modeled
// NoSuchKey (no body, or another code), and a read must still see an absent
// log, not a store failure. A missing bucket and a denied read stay failures,
// so a misconfiguration is not shown as "no logs".
func TestS3StoreGetMatchesNotFoundBeyondTheModeledError(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		notFound bool
	}{
		{"modeled NoSuchKey", http.StatusNotFound, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`, true},
		{"404 without a body", http.StatusNotFound, ``, true},
		{"404 NotFound code", http.StatusNotFound, `<Error><Code>NotFound</Code><Message>missing</Message></Error>`, true},
		{"404 store-specific code", http.StatusNotFound, `<Error><Code>NoSuchObject</Code><Message>missing</Message></Error>`, true},
		{"missing bucket", http.StatusNotFound, `<Error><Code>NoSuchBucket</Code><Message>no bucket</Message></Error>`, false},
		{"access denied", http.StatusForbidden, `<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s3StoreAnswering(t, tc.status, tc.body).Get(context.Background(), "t/d/r/k/1.log")
			if err == nil {
				t.Fatal("Get = nil error, want one")
			}
			if got := errors.Is(err, ErrObjectNotFound); got != tc.notFound {
				t.Fatalf("errors.Is(%v, ErrObjectNotFound) = %v, want %v", err, got, tc.notFound)
			}
		})
	}
}
