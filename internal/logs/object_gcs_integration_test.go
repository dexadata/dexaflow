//go:build integration

package logs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"testing"
	"time"
)

// gcsTestStore returns a GCSStore over a fresh bucket of the GCS emulator at
// DEXAFLOW_TEST_GCS_ENDPOINT (fake-gcs-server in CI), reached by the real
// cloud.google.com/go/storage client through STORAGE_EMULATOR_HOST, which the
// client reads to drop authentication. It skips when the variable is unset.
func gcsTestStore(t *testing.T) *GCSStore {
	t.Helper()
	endpoint := os.Getenv("DEXAFLOW_TEST_GCS_ENDPOINT")
	if endpoint == "" {
		t.Skip("DEXAFLOW_TEST_GCS_ENDPOINT must point at a GCS emulator for this test")
	}
	t.Setenv("STORAGE_EMULATOR_HOST", strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://"))
	ctx := t.Context()
	bucket := fmt.Sprintf("dexaflow-it-%d", time.Now().UnixNano())
	store, err := NewGCSStore(ctx, GCSConfig{Bucket: bucket})
	if err != nil {
		t.Fatalf("NewGCSStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.client.Bucket(bucket).Create(ctx, "dexaflow-it", nil); err != nil {
		t.Fatalf("creating bucket %s: %v", bucket, err)
	}
	return store
}

// The segmented layout against the GCS API (#526 bucket A): the same contract
// the S3 backend is held to, through the real Go client rather than a fake of
// it. The boot check accepts a store that answers a missing object with
// ErrObjectNotExist, a log larger than several segments is written as separate
// segment objects, a reopened attempt appends after the last segment, the
// attempt reads back whole and in order, a delimited list returns each
// directory once, and an attempt that was never written reads as not found.
func TestObjectSinkSegmentedAgainstGCSIntegration(t *testing.T) {
	store := gcsTestStore(t)
	lineLen := len(EncodeLine(fixedEvent("line 000")) + "\n")
	setFlushTuning(t, 2*lineLen, time.Hour)
	setSegmentBytes(t, 4*lineLen)
	ctx := context.Background()
	sink, err := NewDurableSink(ctx, "gcs", "", store, "logs", nil, WithObjectLayout(ObjectLayoutSegmented))
	if err != nil {
		t.Fatalf("NewDurableSink rejected a store that answers ErrObjectNotExist: %v", err)
	}
	obj := sink.(*ObjectSink)
	ref := sampleRef()

	write := func(from, to int) {
		t.Helper()
		w, openErr := obj.Open(ref)
		if openErr != nil {
			t.Fatalf("Open: %v", openErr)
		}
		for i := from; i < to; i++ {
			if writeErr := w.WriteEvent(fixedEvent(fmt.Sprintf("line %03d", i))); writeErr != nil {
				t.Fatalf("WriteEvent(%d): %v", i, writeErr)
			}
		}
		if closeErr := w.Close(); closeErr != nil {
			t.Fatalf("Close: %v", closeErr)
		}
	}
	write(0, 30)
	write(30, 40) // a reopened attempt appends

	keys, err := store.List(ctx, "logs/", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	dir := strings.TrimSuffix(obj.segmentKey(ref, 0), "00000000.log")
	var segments []string
	for _, k := range keys {
		if strings.HasPrefix(k, dir) {
			segments = append(segments, strings.TrimPrefix(k, dir))
		}
	}
	if want := "00000000.log,00000001.log,00000002.log,00000003.log,00000004.log,00000005.log,00000006.log,00000007.log,00000008.log,00000009.log,00000010.log"; strings.Join(segments, ",") != want {
		t.Errorf("segment objects = %v, want segments 0 to 10\nall keys: %v", segments, keys)
	}
	got := messages(readAll(t, obj, ref))
	if len(got) != 40 {
		t.Fatalf("read back %d lines, want 40: %v", len(got), got)
	}
	for i, m := range got {
		if want := fmt.Sprintf("line %03d", i); m != want {
			t.Fatalf("line %d = %q, want %q (out of order or lost across segments)", i, m, want)
		}
	}

	// A delimited list returns the segment directory once, as a prefix, not
	// its eleven objects.
	prefixes, err := store.List(ctx, path.Dir(strings.TrimSuffix(dir, "/"))+"/", "/")
	if err != nil {
		t.Fatalf("delimited List: %v", err)
	}
	if n := strings.Count(strings.Join(prefixes, "\n"), dir); n != 1 {
		t.Errorf("delimited list named the segment directory %d times, want once: %v", n, prefixes)
	}

	missing := ref
	missing.TaskID = "never_ran"
	if rc, err := obj.Read(ctx, missing); err == nil {
		_ = rc.Close()
		t.Error("an attempt that was never written read back without an error")
	}
	if _, err := store.Get(ctx, "logs/no/such/object.log"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("Get of a missing object = %v, want ErrObjectNotFound", err)
	}
}
