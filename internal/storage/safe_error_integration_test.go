//go:build integration

package storage_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestStorageProducesSafeErrorsForClientFacingMessages closes the hole review
// found in #961's fix.
//
// The fix inverts the default from allow to deny: a 4xx/5xx detail is a fixed
// phrase unless the error is a domain.SafeError someone deliberately wrote as
// client-facing. Seven messages were converted to Safef on that basis — the
// ones a tenant needs in order to fix their own input.
//
// Nothing pinned them. Reverting all seven to their pre-fix fmt.Errorf form
// left the whole suite green, because the tests that look client-facing build
// their SafeError values in the test or hand one to a fake repo. They pin
// handleRepoError; they do not pin that any storage call site produces one.
//
// So the promise "the messages worth reading survive" was true on the day and
// unpinned the day after: a refactor could turn a self-service 400 into "the
// request was rejected by a validation rule" with CI green, and the first
// report would be a support ticket. These assertions are at the storage layer
// because that is where the property lives — an API-level test can be satisfied
// by a fake.
func TestStorageProducesSafeErrorsForClientFacingMessages(t *testing.T) {
	repo, _, ctx := openRepo(t)

	assertSafe := func(t *testing.T, err error, class error, want string) {
		t.Helper()
		if err == nil {
			t.Fatal("no error; this call is supposed to be refused")
		}
		var safe *domain.SafeError
		if !errors.As(err, &safe) {
			t.Fatalf("error is not a *domain.SafeError, so its text is redacted from the client: %v", err)
		}
		if !errors.Is(err, class) {
			t.Errorf("error class is not %v: %v", class, err)
		}
		if !strings.Contains(safe.Error(), want) {
			t.Errorf("the message a tenant sees lost %q: %q", want, safe.Error())
		}
	}

	t.Run("unknown role on CreateUser", func(t *testing.T) {
		_, err := repo.CreateUser(ctx, "default", "safe-probe@leoflow.local", "pw-123456", []string{"not-a-real-role"})
		assertSafe(t, err, domain.ErrValidation, "not-a-real-role")
	})

	t.Run("unknown role on CreateOIDCUser", func(t *testing.T) {
		_, err := repo.CreateOIDCUser(ctx, "default", "safe-oidc@leoflow.local", "prov", "subj-1", []string{"not-a-real-role"})
		assertSafe(t, err, domain.ErrValidation, "not-a-real-role")
	})

	t.Run("the default pool cannot be deleted", func(t *testing.T) {
		err := repo.DeletePool(ctx, "default", "default_pool")
		assertSafe(t, err, domain.ErrConflict, "default pool cannot be deleted")
	})

	// The two that matter most: they name the thing to create and the command
	// that creates it. Generalising these turns a self-service fix into a
	// support ticket, which is the cost of erring the other way.
	t.Run("a spec declaring an unknown variable says which, and how to define it", func(t *testing.T) {
		spec := domain.DAGSpec{
			SchemaVersion: "1.0", DagID: "safe_probe_var", DagVersion: "v1", Image: "img:v1",
			Variables: []string{"no_such_variable_here"},
			Tasks:     []domain.TaskSpec{{TaskID: "t", Type: "python"}},
		}
		hash, herr := spec.CanonicalHash()
		if herr != nil {
			t.Fatal(herr)
		}
		_, err := repo.RegisterDagVersion(ctx, "default", spec, hash)
		assertSafe(t, err, domain.ErrValidation, "no_such_variable_here")
		var safe *domain.SafeError
		if errors.As(err, &safe) && !strings.Contains(safe.Error(), "leoflow variables set") {
			t.Errorf("the message no longer tells the author how to fix it: %q", safe.Error())
		}
	})

	t.Run("a spec declaring an unknown connection says which, and how to define it", func(t *testing.T) {
		spec := domain.DAGSpec{
			SchemaVersion: "1.0", DagID: "safe_probe_conn", DagVersion: "v1", Image: "img:v1",
			Connections: []string{"no_such_connection_here"},
			Tasks:       []domain.TaskSpec{{TaskID: "t", Type: "python"}},
		}
		hash, herr := spec.CanonicalHash()
		if herr != nil {
			t.Fatal(herr)
		}
		_, err := repo.RegisterDagVersion(ctx, "default", spec, hash)
		assertSafe(t, err, domain.ErrValidation, "no_such_connection_here")
		var safe *domain.SafeError
		if errors.As(err, &safe) && !strings.Contains(safe.Error(), "leoflow connections set") {
			t.Errorf("the message no longer tells the author how to fix it: %q", safe.Error())
		}
	})
}
