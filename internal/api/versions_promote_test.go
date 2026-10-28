package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestRegisterVersionPromotedFrom: promoted_from and promoted_from_server
// reach the repository as the version's provenance, on the register and on
// its dry run; a register without them names no source; a bad value is
// refused before anything is registered (#1708).
func TestRegisterVersionPromotedFrom(t *testing.T) {
	for _, route := range []string{"/versions", "/versions/dry_run"} {
		for _, tc := range []struct {
			name, query        string
			wantCode           int
			wantTarget, wantSv string
			wantNone           bool
		}{
			{name: "none", query: "", wantCode: 0, wantNone: true},
			{name: "both", query: "?promoted_from=staging&promoted_from_server=https%3A%2F%2Fstaging.example.com",
				wantTarget: "staging", wantSv: "https://staging.example.com"},
			{name: "target only", query: "?promoted_from=dev", wantTarget: "dev"},
			{name: "server without target", query: "?promoted_from_server=https%3A%2F%2Fdev.example", wantCode: http.StatusBadRequest},
			{name: "empty target", query: "?promoted_from=", wantCode: http.StatusBadRequest},
			{name: "control character", query: "?promoted_from=a%0Ab", wantCode: http.StatusBadRequest},
			{name: "too long", query: "?promoted_from=" + strings.Repeat("x", 64), wantCode: http.StatusBadRequest},
			{name: "longest name", query: "?promoted_from=" + strings.Repeat("x", 63), wantTarget: strings.Repeat("x", 63)},
			{name: "upper case", query: "?promoted_from=Prod", wantCode: http.StatusBadRequest},
			{name: "bidi override", query: "?promoted_from=dev%E2%80%AE", wantCode: http.StatusBadRequest},
			{name: "leading dash", query: "?promoted_from=-dev", wantCode: http.StatusBadRequest},
			{name: "long server URL", query: "?promoted_from=dev&promoted_from_server=https%3A%2F%2Fdev.example%2F" + strings.Repeat("p", 1900),
				wantTarget: "dev", wantSv: "https://dev.example/" + strings.Repeat("p", 1900)},
			{name: "server too long", query: "?promoted_from=dev&promoted_from_server=https%3A%2F%2Fdev.example%2F" + strings.Repeat("p", 2049), wantCode: http.StatusBadRequest},
			{name: "server format character", query: "?promoted_from=dev&promoted_from_server=https%3A%2F%2Fdev.example%2F%E2%80%AEx", wantCode: http.StatusBadRequest},
			{name: "server control character", query: "?promoted_from=dev&promoted_from_server=https%3A%2F%2Fdev.example%2F%09x", wantCode: http.StatusBadRequest},
			{name: "server ftp", query: "?promoted_from=dev&promoted_from_server=ftp%3A%2F%2Fdev.example", wantCode: http.StatusBadRequest},
			{name: "server not a URL", query: "?promoted_from=dev&promoted_from_server=not-a-url", wantCode: http.StatusBadRequest},
			{name: "server with credentials", query: "?promoted_from=dev&promoted_from_server=https%3A%2F%2Fu%3Ap%40dev.example", wantCode: http.StatusBadRequest},
		} {
			t.Run(route+" "+tc.name, func(t *testing.T) {
				repo := &fakeVersionRepo{created: true}
				rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl"+route+tc.query, validSpecJSON)
				if tc.wantCode == http.StatusBadRequest {
					if rec.Code != http.StatusBadRequest {
						t.Fatalf("register = %d, want 400 (%s)", rec.Code, rec.Body.String())
					}
					if !strings.Contains(rec.Body.String(), "promoted_from") {
						t.Errorf("refusal %s does not name promoted_from", rec.Body.String())
					}
					if len(repo.promoted) != 0 {
						t.Errorf("a refused query still registered")
					}
					return
				}
				if rec.Code >= http.StatusMultipleChoices {
					t.Fatalf("register = %d (%s)", rec.Code, rec.Body.String())
				}
				if len(repo.promoted) != 1 {
					t.Fatalf("registers = %d, want 1", len(repo.promoted))
				}
				got := repo.promoted[0]
				if tc.wantNone {
					if got != nil {
						t.Errorf("promoted_from = %+v, want none", got)
					}
					return
				}
				if got == nil || got.Target != tc.wantTarget || got.Server != tc.wantSv {
					t.Errorf("promoted_from = %+v, want target %q server %q", got, tc.wantTarget, tc.wantSv)
				}
			})
		}
	}
}
