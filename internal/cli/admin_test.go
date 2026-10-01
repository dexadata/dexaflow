package cli

import (
	"encoding/json"
	"net/http"
	"testing"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// writeJSON encodes v as a JSON response body for the fake control planes used
// by the admin command tests.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encoding response: %v", err)
	}
}

// newTestClient builds a typed /api/v2 client aimed at a fake server.
func newTestClient(t *testing.T, baseURL string) *apiclient.ClientWithResponses {
	t.Helper()
	c, err := apiclient.New(baseURL, "")
	if err != nil {
		t.Fatalf("apiclient.New: %v", err)
	}
	return c
}

func strptr(s string) *string { return &s }
func boolptr(b bool) *bool    { return &b }
