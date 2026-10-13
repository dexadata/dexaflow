package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeOCIRegistry is a registry that answers every manifest request, for any
// repository and tag, with one manifest, as a registry would after the stub
// builder's push. It records the references it was asked for.
type fakeOCIRegistry struct {
	host, digest string
	mu           sync.Mutex
	reads        []string
}

// newFakeOCIRegistry serves manifest and returns the registry; its digest is
// the sha256 of manifest, as a real registry computes it.
func newFakeOCIRegistry(t *testing.T, manifest string) *fakeOCIRegistry {
	t.Helper()
	sum := sha256.Sum256([]byte(manifest))
	r := &fakeOCIRegistry{digest: "sha256:" + hex.EncodeToString(sum[:])}
	mediaType := "application/vnd.oci.image.index.v1+json"
	var m struct {
		MediaType string `json:"mediaType"` //nolint:tagliatelle // the OCI manifest field name
	}
	if json.Unmarshal([]byte(manifest), &m) == nil && m.MediaType != "" {
		mediaType = m.MediaType
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v2/" {
			return
		}
		repo, ref, ok := strings.Cut(strings.TrimPrefix(req.URL.Path, "/v2/"), "/manifests/")
		if !ok {
			http.NotFound(w, req)
			return
		}
		r.mu.Lock()
		r.reads = append(r.reads, repo+":"+ref)
		r.mu.Unlock()
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Docker-Content-Digest", r.digest)
		if req.Method != http.MethodHead {
			_, _ = w.Write([]byte(manifest))
		}
	}))
	t.Cleanup(srv.Close)
	r.host = strings.TrimPrefix(srv.URL, "http://")
	return r
}

// readCount returns how many manifest reads the registry answered.
func (r *fakeOCIRegistry) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reads)
}

// singleManifest is a one-platform image manifest.
const singleManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{},"layers":[]}`

// missingRegistry is a registry that has no image at all.
func missingRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/v2/" {
			return
		}
		http.Error(w, `{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}
