//go:build integration

package storage_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRegisterAuditsTheDigestRequirement: while deploy.require_digest is
// warn or enforce, the register's audit entry records the mode and whether
// the image is pinned by digest; while it is off, the entry is the one it
// always was (#1703).
func TestRegisterAuditsTheDigestRequirement(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("require_digest_%d", time.Now().UnixNano())
	pinned := "ghcr.io/acme/etl@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		version, image string
		mode           domain.DigestRequirement
		want           map[string]any // nil: neither field is recorded
	}{
		{"v1", "ghcr.io/acme/etl:v1", domain.DigestRequireOff, nil},
		{"v2", "ghcr.io/acme/etl:v2", domain.DigestRequireWarn, map[string]any{"require_digest": "warn", "image_pinned": false}},
		{"v3", pinned, domain.DigestRequireEnforce, map[string]any{"require_digest": "enforce", "image_pinned": true}},
	}
	for i, tc := range cases {
		spec := registerTxSpec(dagID, tc.version, fmt.Sprint(i), 3, false)
		spec.Image = tc.image
		hash, err := spec.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		got, err := repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{Spec: spec, SpecHash: hash, RequireDigest: tc.mode})
		if err != nil || !got.Created {
			t.Fatalf("%s: register = %+v, %v", tc.version, got, err)
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(latestAudit(t, repo, dagID, "dag.version.register").Extra), &meta); err != nil {
			t.Fatal(err)
		}
		if meta["version"] != tc.version {
			t.Fatalf("%s: latest register audit is %v", tc.version, meta)
		}
		for _, k := range []string{"require_digest", "image_pinned"} {
			want, recorded := tc.want[k]
			if have, ok := meta[k]; ok != recorded || have != want {
				t.Errorf("%s: audit %s = %v (present %v), want %v (present %v)", tc.version, k, have, ok, want, recorded)
			}
		}
	}
}
