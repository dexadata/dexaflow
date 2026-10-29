//go:build integration

package logs

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// s3TestStore returns an S3Store over a fresh bucket of the S3-compatible
// store at DEXAFLOW_TEST_S3_ENDPOINT (LocalStack in CI, MinIO or Ceph work the
// same), reached with static keys and path-style addressing as an on-prem
// install would be. It skips when the variable is unset.
func s3TestStore(t *testing.T) *S3Store {
	t.Helper()
	endpoint := os.Getenv("DEXAFLOW_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("DEXAFLOW_TEST_S3_ENDPOINT must point at an S3-compatible store for this test")
	}
	ctx := t.Context()
	bucket := fmt.Sprintf("dexaflow-it-%d", time.Now().UnixNano())
	store, err := NewS3Store(ctx, S3Config{
		Bucket: bucket, Region: "us-east-1", Endpoint: endpoint, ForcePathStyle: true,
		AccessKeyID: "test", SecretAccessKey: "test",
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	if _, err := store.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("creating bucket %s: %v", bucket, err)
	}
	return store
}

// The segmented layout against a real S3 API (#1336, #1616): the boot check
// accepts a store that answers a missing key with NoSuchKey, a log larger than
// several segments is written as separate segment objects, each upload stays
// within one segment plus one flush, and the attempt reads back whole and in
// order. A reopened attempt appends after the last segment, and an attempt
// that was never written reads as not found.
func TestObjectSinkSegmentedAgainstS3Integration(t *testing.T) {
	store := s3TestStore(t)
	lineLen := len(EncodeLine(fixedEvent("line 000")) + "\n")
	setFlushTuning(t, 2*lineLen, time.Hour)
	setSegmentBytes(t, 4*lineLen)
	ctx := context.Background()
	sink, err := NewDurableSink(ctx, "s3", "", store, "logs", nil, WithObjectLayout(ObjectLayoutSegmented))
	if err != nil {
		t.Fatalf("NewDurableSink rejected a store that answers NoSuchKey: %v", err)
	}
	obj := sink.(*ObjectSink)
	ref := sampleRef()

	write := func(from, to int) {
		t.Helper()
		w, err := obj.Open(ref)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		for i := from; i < to; i++ {
			if err := w.WriteEvent(fixedEvent(fmt.Sprintf("line %03d", i))); err != nil {
				t.Fatalf("WriteEvent(%d): %v", i, err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	write(0, 30)
	write(30, 40) // a reopened attempt appends

	keys, err := store.List(ctx, "logs/", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Four lines per segment under {try}.log.d/: the first stream's 30 lines
	// fill segments 0 to 7, and the reopened stream starts after the last one
	// it finds (the writer's probe for the first missing segment), so its 10
	// lines are segments 8 to 10.
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
	missing := ref
	missing.TaskID = "never_ran"
	if rc, err := obj.Read(ctx, missing); err == nil {
		_ = rc.Close()
		t.Error("an attempt that was never written read back without an error")
	}
}
