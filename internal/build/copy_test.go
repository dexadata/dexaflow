package build

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
)

// Copy moves an image from one registry to another by digest: the
// destination tag then names the same digest, so a promoted version runs the
// same bytes (#1708).
func TestRegistryCopiesAnImageByDigest(t *testing.T) {
	src, dst := testRegistry(t), testRegistry(t)
	for name, digest := range map[string]string{
		"image": pushImage(t, src+"/dev/etl:v1").String(),
		"index": pushIndex(t, src+"/dev/etl:v2", "linux/amd64", "linux/arm64").String(),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			got, err := Registry{}.Copy(ctx, src+"/dev/etl@"+digest, dst+"/prod/etl", "v-"+name)
			if err != nil {
				t.Fatalf("Copy: %v", err)
			}
			if want := dst + "/prod/etl@" + digest; got != want {
				t.Errorf("Copy = %q, want %q", got, want)
			}
			pinned, err := Registry{}.Resolve(ctx, dst+"/prod/etl:v-"+name)
			if err != nil {
				t.Fatalf("Resolve the copy: %v", err)
			}
			if pinned.Ref != dst+"/prod/etl@"+digest {
				t.Errorf("destination tag names %q, want digest %s", pinned.Ref, digest)
			}
		})
	}
}

// Without a tag the copy is pushed by digest alone, and copying an image the
// destination already holds succeeds again.
func TestRegistryCopyWithoutATagAndTwice(t *testing.T) {
	src, dst := testRegistry(t), testRegistry(t)
	d := pushImage(t, src+"/dev/etl:v1").String()
	for i := 0; i < 2; i++ {
		got, err := Registry{}.Copy(context.Background(), src+"/dev/etl@"+d, dst+"/prod/etl", "")
		if err != nil {
			t.Fatalf("Copy %d: %v", i, err)
		}
		if got != dst+"/prod/etl@"+d {
			t.Errorf("Copy %d = %q", i, got)
		}
		if _, err := (Registry{}).Resolve(context.Background(), got); err != nil {
			t.Errorf("the copy %d is not in the destination: %v", i, err)
		}
	}
}

// Only an image pinned by digest is copied: a tag can move between the read
// and the copy.
func TestRegistryCopyRefusesATag(t *testing.T) {
	src, dst := testRegistry(t), testRegistry(t)
	pushImage(t, src+"/dev/etl:v1")
	_, err := Registry{}.Copy(context.Background(), src+"/dev/etl:v1", dst+"/prod/etl", "v1")
	if err == nil || !strings.Contains(err.Error(), "pinned by digest") {
		t.Fatalf("Copy of a tag = %v, want a refusal naming the digest", err)
	}
}

// A destination that is not a repository (it carries a tag or a digest) is
// refused before anything is read.
func TestRegistryCopyRefusesADestinationWithATag(t *testing.T) {
	src, dst := testRegistry(t), testRegistry(t)
	d := pushImage(t, src+"/dev/etl:v1").String()
	_, err := Registry{}.Copy(context.Background(), src+"/dev/etl@"+d, dst+"/prod/etl:v1", "")
	if err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("Copy to a tagged destination = %v, want a refusal", err)
	}
}

// A destination tag that already names another digest is never moved: the
// copy is refused and the tag keeps its image.
func TestRegistryCopyRefusesATagOfAnotherDigest(t *testing.T) {
	src, dst := testRegistry(t), testRegistry(t)
	d := pushImage(t, src+"/dev/etl:v1").String()
	other := pushImage(t, dst+"/prod/etl:v1").String()
	_, err := Registry{}.Copy(context.Background(), src+"/dev/etl@"+d, dst+"/prod/etl", "v1")
	if err == nil || !strings.Contains(err.Error(), other) {
		t.Fatalf("Copy over a tag of another digest = %v, want a refusal naming %s", err, other)
	}
	if p, rerr := (Registry{}).Resolve(context.Background(), dst+"/prod/etl:v1"); rerr != nil || !strings.HasSuffix(p.Ref, other) {
		t.Errorf("the tag moved: %q (%v), want %s", p.Ref, rerr, other)
	}
	// The same digest under the tag already is fine: a promote run twice.
	if _, err := (Registry{}).Copy(context.Background(), src+"/dev/etl@"+d, dst+"/prod/etl2", "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := (Registry{}).Copy(context.Background(), src+"/dev/etl@"+d, dst+"/prod/etl2", "v1"); err != nil {
		t.Errorf("copying again under a tag of the same digest: %v", err)
	}
}

// tamperingRegistry serves a registry and, when on, changes the bytes of
// every blob or of every manifest it serves, as a compromised mirror would.
type tamperingRegistry struct {
	next            http.Handler
	blobs, manifest bool
}

func (r *tamperingRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	tamper := (r.blobs && strings.Contains(req.URL.Path, "/blobs/sha256:")) ||
		(r.manifest && strings.Contains(req.URL.Path, "/manifests/"))
	if !tamper || req.Method != http.MethodGet {
		r.next.ServeHTTP(w, req)
		return
	}
	rec := httptest.NewRecorder()
	r.next.ServeHTTP(rec, req)
	body := rec.Body.Bytes()
	if len(body) > 0 {
		if r.manifest {
			body = append(append([]byte(nil), body[:len(body)-1]...), ' ', body[len(body)-1])
		} else {
			body = append([]byte(nil), body...)
			body[len(body)/2] ^= 0xff
		}
	}
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(rec.Code)
	_, _ = w.Write(body)
}

// Bytes that do not match their digest are never copied: a tampered layer
// or a manifest whose digest is not the pinned one fails the copy, and the
// destination holds nothing.
func TestRegistryCopyRefusesTamperedBytes(t *testing.T) {
	for _, tc := range []struct {
		name            string
		blobs, manifest bool
	}{{"blob", true, false}, {"manifest", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &tamperingRegistry{next: registry.New()}
			srv := httptest.NewServer(tr)
			t.Cleanup(srv.Close)
			src, dst := strings.TrimPrefix(srv.URL, "http://"), testRegistry(t)
			d := pushImage(t, src+"/dev/etl:v1").String()
			tr.blobs, tr.manifest = tc.blobs, tc.manifest
			if _, err := (Registry{}).Copy(context.Background(), src+"/dev/etl@"+d, dst+"/prod/etl", "v1"); err == nil {
				t.Fatal("Copy of tampered bytes succeeded")
			}
			if _, err := (Registry{}).Resolve(context.Background(), dst+"/prod/etl@"+d); err == nil {
				t.Error("the destination holds the image")
			}
		})
	}
}

// A destination that accepts the push but then does not hold the image
// (it answers the digest with 404) fails the copy: it is checked after the
// write, not assumed.
func TestRegistryCopyChecksTheDestinationAfterTheWrite(t *testing.T) {
	src := testRegistry(t)
	d := pushImage(t, src+"/dev/etl:v1").String()
	reg := registry.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/manifests/"+d) {
			http.NotFound(w, r)
			return
		}
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	dst := strings.TrimPrefix(srv.URL, "http://")
	if _, err := (Registry{}).Copy(context.Background(), src+"/dev/etl@"+d, dst+"/prod/etl", ""); err == nil || !strings.Contains(err.Error(), "checking the copy") {
		t.Fatalf("Copy = %v, want the check after the write to fail", err)
	}
}
