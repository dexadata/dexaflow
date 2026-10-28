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
	// uploads counts the blob uploads started (a push, or a push check);
	// denyPush answers them 401, as a registry does without a credential.
	uploads  int
	denyPush bool
	// basicAuth, when set, is the user:pass every request but the ping
	// must carry; others get 401, as a private registry answers.
	basicAuth string
	// seen are the Basic credentials requests carried.
	seen []string
}

// credentials returns the Basic credentials sent so far.
func (r *fakeOCIRegistry) credentials() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// authorized reports whether req carries the credential basicAuth asks for.
func (r *fakeOCIRegistry) authorized(req *http.Request) bool {
	u, p, ok := req.BasicAuth()
	if ok {
		r.mu.Lock()
		r.seen = append(r.seen, u+":"+p)
		r.mu.Unlock()
	}
	if r.basicAuth == "" {
		return true
	}
	return ok && u+":"+p == r.basicAuth
}

// serveUpload answers the start and the cancel of a blob upload.
func (r *fakeOCIRegistry) serveUpload(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case req.Method == http.MethodPost && r.denyPush:
		r.uploads++
		http.Error(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`, http.StatusUnauthorized)
	case req.Method == http.MethodPost:
		r.uploads++
		w.Header().Set("Location", req.URL.Path+"u1")
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// uploadCount returns how many blob uploads were started.
func (r *fakeOCIRegistry) uploadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.uploads
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
		if !r.authorized(req) {
			w.Header().Set("WWW-Authenticate", `Basic realm="fake"`)
			http.Error(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`, http.StatusUnauthorized)
			return
		}
		if req.URL.Path == "/v2/" {
			return
		}
		if strings.Contains(req.URL.Path, "/blobs/uploads/") {
			r.serveUpload(w, req)
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

// readList returns the repo:ref of every manifest read so far.
func (r *fakeOCIRegistry) readList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reads...)
}
