//go:build integration

package storage_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRegisterPromotedVersionAuditsItsSource: a register that names the
// control plane the version was promoted from records it in the register's
// audit entry; a plain register records no source (#1708).
func TestRegisterPromotedVersionAuditsItsSource(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("promoted_%d", time.Now().UnixNano())
	v1 := registerTxSpec(dagID, "v1", "a", 3, false)
	registerTx(t, ctx, repo, v1)
	var plain map[string]any
	if err := json.Unmarshal([]byte(latestAudit(t, repo, dagID, "dag.version.register").Extra), &plain); err != nil {
		t.Fatal(err)
	}
	if _, ok := plain["promoted_from"]; ok {
		t.Errorf("plain register audit %v names a source", plain)
	}

	v2 := registerTxSpec(dagID, "v2", "bb", 3, false)
	hash, err := v2.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{
		Spec: v2, SpecHash: hash,
		PromotedFrom: &domain.DagPromotion{Target: "staging", Server: "https://staging.example.com"},
	})
	if err != nil || !got.Created {
		t.Fatalf("promoted register = %+v, %v", got, err)
	}
	var meta struct {
		Version      string            `json:"version"`
		PromotedFrom map[string]string `json:"promoted_from"`
	}
	if err := json.Unmarshal([]byte(latestAudit(t, repo, dagID, "dag.version.register").Extra), &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Version != "v2" || meta.PromotedFrom["target"] != "staging" || meta.PromotedFrom["server"] != "https://staging.example.com" {
		t.Errorf("promoted register audit = %+v, want version v2 promoted from staging at https://staging.example.com", meta)
	}

	// What GET /dags/{dag_id}/spec?version_number=N serves hashes as the
	// registered spec did, so a promoted copy is the same version.
	_, served, err := repo.GetDagVersionSpec(ctx, "default", dagID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if again, herr := served.CanonicalHash(); herr != nil || again != hash {
		t.Errorf("served spec hash = %s (%v), want the registered %s", again, herr, hash)
	}
}
