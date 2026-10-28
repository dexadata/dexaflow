//go:build integration

package storage_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRegisterDagVersionRecordsBuildInfo: the build info of a register is
// stored with the version it creates, listed with it and recorded in the
// register's audit entry (#1702). A register without it lists none. A
// re-push of the same content from another build creates nothing and keeps
// the first build info: a version is immutable.
func TestRegisterDagVersionRecordsBuildInfo(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("build_info_%d", time.Now().UnixNano())
	info := domain.BuildInfo{
		Commit: "0123456789abcdef0123456789abcdef01234567", Repo: "https://github.com/acme/etl",
		PipelineURL: "https://github.com/acme/etl/actions/runs/42", Builder: "code-layer",
		BaseImage: "ghcr.io/dexadata/dexaflow-runtime:py3.12-v0.5.4", CLIVersion: "v0.5.4",
	}
	v1 := registerTxSpec(dagID, "v1", "a", 3, false)
	h1, err := v1.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if got, rerr := repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{Spec: v1, SpecHash: h1, BuildInfo: &info}); rerr != nil || !got.Created {
		t.Fatalf("register v1: %+v %v", got, rerr)
	}
	listed := versionByLabel(t, repo, "default", dagID, "v1")
	if listed.BuildInfo == nil || *listed.BuildInfo != info {
		t.Fatalf("listed build info = %+v, want %+v", listed.BuildInfo, info)
	}
	entry := latestAudit(t, repo, dagID, "dag.version.register")
	var meta struct {
		BuildInfo domain.BuildInfo `json:"build_info"`
	}
	if jerr := json.Unmarshal([]byte(entry.Extra), &meta); jerr != nil || meta.BuildInfo != info {
		t.Errorf("audit metadata %s, want build_info %+v (%v)", entry.Extra, info, jerr)
	}

	other := info
	other.Commit, other.PipelineURL = "fedcba9", "https://github.com/acme/etl/actions/runs/43"
	if got, rerr := repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{Spec: v1, SpecHash: h1, BuildInfo: &other}); rerr != nil || got.Created {
		t.Fatalf("re-push v1: %+v %v", got, rerr)
	}
	if again := versionByLabel(t, repo, "default", dagID, "v1"); again.BuildInfo == nil || *again.BuildInfo != info {
		t.Errorf("re-push changed build info to %+v, want %+v", again.BuildInfo, info)
	}

	v2 := registerTxSpec(dagID, "v2", "b", 3, false)
	h2, err := v2.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if got, rerr := repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{Spec: v2, SpecHash: h2}); rerr != nil || !got.Created {
		t.Fatalf("register v2: %+v %v", got, rerr)
	}
	if plain := versionByLabel(t, repo, "default", dagID, "v2"); plain.BuildInfo != nil {
		t.Errorf("a register without build info lists %+v", plain.BuildInfo)
	}
	change, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, "", admitAll)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if change.To.BuildInfo == nil || *change.To.BuildInfo != info {
		t.Errorf("rollback target build info = %+v, want %+v", change.To.BuildInfo, info)
	}
}
