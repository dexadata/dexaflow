package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// usersServer serves a fixed two-user collection at /api/v2/users so the
// listing and rendering can be exercised end-to-end.
func usersServer(t *testing.T) *httptest.Server {
	t.Helper()
	created := time.Now().Add(-48 * time.Hour)
	users := []apiclient.UserListItem{
		{Id: "u-1", Email: "alice@example.com", Roles: []string{"admin", "operator"}, IsActive: true, CreatedAt: created},
		{Id: "u-2", Email: "bob@example.com", Roles: []string{"viewer"}, IsActive: false, CreatedAt: created},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/users", func(w http.ResponseWriter, _ *http.Request) {
		total := len(users)
		writeJSON(t, w, apiclient.UserCollection{Users: &users, TotalEntries: &total})
	})
	return httptest.NewServer(mux)
}

// TestAdminUsersList drives the command end-to-end against a fake control
// plane and asserts the seeded accounts (and their roles) reach stdout.
func TestAdminUsersList(t *testing.T) {
	srv := usersServer(t)
	defer srv.Close()

	out, _, err := run(t, "admin", "users", "list", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("admin users list: %v", err)
	}
	for _, want := range []string{"alice@example.com", "bob@example.com", "admin,operator", "viewer"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to include %q", out, want)
		}
	}
}

// TestCollectUsers pins the client-facing collection helper: it honors the
// limit/offset it is handed and returns the decoded accounts.
func TestCollectUsers(t *testing.T) {
	srv := usersServer(t)
	defer srv.Close()

	users, err := collectUsers(context.Background(), newTestClient(t, srv.URL), 100, 0)
	if err != nil {
		t.Fatalf("collectUsers: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("collectUsers returned %d users, want 2", len(users))
	}
	if users[0].Email != "alice@example.com" {
		t.Errorf("first user email = %q, want alice@example.com", users[0].Email)
	}
}

// TestRenderUsers pins the table shape and the comma-joined roles column
// without a server in the loop.
func TestRenderUsers(t *testing.T) {
	users := []apiclient.UserListItem{
		{Id: "u-1", Email: "alice@example.com", Roles: []string{"admin", "operator"}, IsActive: true, CreatedAt: time.Now()},
	}
	var buf bytes.Buffer
	if err := renderUsers(&buf, users); err != nil {
		t.Fatalf("renderUsers: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"EMAIL", "ID", "ROLES", "ACTIVE", "AGE", "alice@example.com", "admin,operator", "true"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered table = %q, want it to include %q", out, want)
		}
	}
}

// TestRenderUsersEmpty covers the empty control plane (e.g. a Lite install):
// an empty collection is a friendly note, never an error.
func TestRenderUsersEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderUsers(&buf, nil); err != nil {
		t.Fatalf("renderUsers(empty): %v", err)
	}
	if !strings.Contains(buf.String(), "No users found.") {
		t.Errorf("empty render = %q, want a friendly no-users note", buf.String())
	}
}

// TestAdminUsersRegistered pins that the `users` subcommand is wired under the
// admin tree, with its own `list` child.
func TestAdminUsersRegistered(t *testing.T) {
	admin := newAdminCommand()
	var users *cobra.Command
	for _, c := range admin.Commands() {
		if c.Name() == "users" {
			users = c
			break
		}
	}
	if users == nil {
		t.Fatal("admin command is missing the `users` subcommand")
	}
	found := false
	for _, c := range users.Commands() {
		if c.Name() == "list" {
			found = true
		}
	}
	if !found {
		t.Error("admin users is missing the `list` subcommand")
	}
}
