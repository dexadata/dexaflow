package build

import (
	"context"
	"strings"
	"testing"
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
