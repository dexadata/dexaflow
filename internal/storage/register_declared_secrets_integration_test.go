//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
)

// withTestCipher configures a deterministic AES-GCM cipher on the repo so
// connection seeding (which encrypts at rest, ADR 0019) works in the test.
func withTestCipher(t *testing.T, repo interface{ SetCipher(secrets.Cipher) }) {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	c, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(c)
}

// registerDeclaring registers a DAG that declares the given variable/connection
// names and returns the RegisterDagVersion error (nil on success).
func registerDeclaring(t *testing.T, repo interface {
	RegisterDagVersion(context.Context, string, domain.DAGSpec, string) (bool, error)
}, ctx context.Context, dagID string, vars, conns []string) error {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Variables:   vars,
		Connections: conns,
		Tasks:       []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash)
	return rerr
}

// A DAG that declares a variable or connection name that does not exist for the
// tenant is rejected at registration (ADR 0055 D6). The error names the offender.
func TestRegisterRejectsUnknownDeclaredNamesIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	withTestCipher(t, repo)
	suffix := time.Now().UnixNano()

	// Seed one real variable and one real connection.
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "greeting", Value: "hi"}); err != nil {
		t.Fatalf("seed variable: %v", err)
	}
	if err := repo.SetConnection(ctx, "default", domain.Connection{ConnID: "warehouse", ConnType: "postgres", Host: "db"}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	t.Run("unknown variable", func(t *testing.T) {
		err := registerDeclaring(t, repo, ctx, fmt.Sprintf("dec_uv_%d", suffix), []string{"greeting", "nope_var"}, nil)
		if err == nil {
			t.Fatal("expected registration to fail for an unknown declared variable")
		}
		if !strings.Contains(err.Error(), "nope_var") {
			t.Errorf("error %q should name the unknown variable 'nope_var'", err.Error())
		}
	})

	t.Run("unknown connection", func(t *testing.T) {
		err := registerDeclaring(t, repo, ctx, fmt.Sprintf("dec_uc_%d", suffix), nil, []string{"warehouse", "nope_conn"})
		if err == nil {
			t.Fatal("expected registration to fail for an unknown declared connection")
		}
		if !strings.Contains(err.Error(), "nope_conn") {
			t.Errorf("error %q should name the unknown connection 'nope_conn'", err.Error())
		}
	})
}

// A valid declaration (all names exist) and an empty declaration both register
// cleanly. The empty case is the Lite/back-compat safety: no existing DAG
// declares anything, so none is rejected.
func TestRegisterAcceptsValidAndEmptyDeclarationsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	withTestCipher(t, repo)
	suffix := time.Now().UnixNano()

	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: "greeting", Value: "hi"}); err != nil {
		t.Fatalf("seed variable: %v", err)
	}
	if err := repo.SetConnection(ctx, "default", domain.Connection{ConnID: "warehouse", ConnType: "postgres", Host: "db"}); err != nil {
		t.Fatalf("seed connection: %v", err)
	}

	if err := registerDeclaring(t, repo, ctx, fmt.Sprintf("dec_ok_%d", suffix), []string{"greeting"}, []string{"warehouse"}); err != nil {
		t.Fatalf("valid declaration should register: %v", err)
	}
	if err := registerDeclaring(t, repo, ctx, fmt.Sprintf("dec_empty_%d", suffix), nil, nil); err != nil {
		t.Fatalf("empty declaration should register: %v", err)
	}
}

// Per-task narrowing declarations are also checked: an unknown name declared
// only at the task level is rejected too (ADR 0055 D6).
func TestRegisterRejectsUnknownTaskLevelDeclarationIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	withTestCipher(t, repo)
	suffix := time.Now().UnixNano()

	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: fmt.Sprintf("dec_task_%d", suffix), DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{
			TaskID:    "t",
			Type:      domain.TaskTypePython,
			Variables: []string{"task_only_unknown"},
		}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	_, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash)
	if rerr == nil {
		t.Fatal("expected registration to fail for an unknown task-level declared variable")
	}
	if !strings.Contains(rerr.Error(), "task_only_unknown") {
		t.Errorf("error %q should name the unknown variable", rerr.Error())
	}
}
