package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestTokenReturnsAccessToken(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"access_token":"jwt-xyz","token_type":"bearer"}`)
	}))
	defer srv.Close()

	tok, err := requestToken(context.Background(), srv.URL, "admin", "pw")
	if err != nil {
		t.Fatalf("requestToken: %v", err)
	}
	if tok != "jwt-xyz" {
		t.Errorf("token = %q, want jwt-xyz", tok)
	}
}

func TestRequestTokenRejectsBadStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	if _, err := requestToken(context.Background(), srv.URL, "admin", "wrong"); err == nil {
		t.Error("expected error on 401")
	}
}

func TestCreateUserSendsRoleAndReturnsUser(t *testing.T) {
	t.Parallel()
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"u-9","email":"alice@example.com","roles":["admin"],"is_active":true}`)
	}))
	defer srv.Close()

	user, err := createUser(context.Background(), srv.URL, "admin-jwt", "alice@example.com", "pw-12345678", []string{"admin"})
	if err != nil {
		t.Fatalf("createUser: %v", err)
	}
	if user.Id != "u-9" || user.Email != "alice@example.com" {
		t.Errorf("unexpected user: %+v", user)
	}
	if len(user.Roles) != 1 || user.Roles[0] != "admin" {
		t.Errorf("unexpected roles: %v", user.Roles)
	}
	if gotAuth != "Bearer admin-jwt" {
		t.Errorf("Authorization = %q, want the admin bearer", gotAuth)
	}
	if !strings.Contains(gotBody, `"roles":["admin"]`) {
		t.Errorf("request body missing roles: %s", gotBody)
	}
}

func TestCreateUserRejectsBadStatus(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"detail":"resource already exists"}`)
	}))
	defer srv.Close()

	if _, err := createUser(context.Background(), srv.URL, "t", "dupe@example.com", "pw-12345678", nil); err == nil {
		t.Error("expected error on 409")
	}
}

func TestCreateTokenCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"jwt-abc"}`)
	}))
	defer srv.Close()

	out, _, err := run(t, "auth", "create-token", "--server", srv.URL, "--username", "a", "--password", "b")
	if err != nil {
		t.Fatalf("create-token: %v", err)
	}
	if !strings.Contains(out, "jwt-abc") {
		t.Errorf("output = %q, want to contain the token", out)
	}
}
