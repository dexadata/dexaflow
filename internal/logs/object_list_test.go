package logs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

// wantTryListing is what both stores must return for the fake listing below:
// the plain objects and the common prefix of a segmented execution.
var wantTryListing = []string{"t/d/r/x/1.e2.log", "t/d/r/x/1.log", "t/d/r/x/1.log.d/"}

// TestS3StoreListsKeysAndCommonPrefixes: one ListObjectsV2 call with the
// delimiter returns the try's objects and each segment directory once.
func TestS3StoreListsKeysAndCommonPrefixes(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
<Name>task-logs</Name><Prefix>t/d/r/x/1.</Prefix><KeyCount>3</KeyCount><IsTruncated>false</IsTruncated>
<Contents><Key>t/d/r/x/1.log</Key></Contents>
<Contents><Key>t/d/r/x/1.e2.log</Key></Contents>
<CommonPrefixes><Prefix>t/d/r/x/1.log.d/</Prefix></CommonPrefixes>
</ListBucketResult>`)
	}))
	defer srv.Close()
	store, err := NewS3Store(context.Background(), S3Config{
		Bucket: "task-logs", Region: "us-east-1", Endpoint: srv.URL, ForcePathStyle: true,
		AccessKeyID: "k", SecretAccessKey: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.List(context.Background(), "t/d/r/x/1.", "/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(wantTryListing) {
		t.Errorf("List = %v, want %v", got, wantTryListing)
	}
	if query == "" {
		t.Fatal("no request reached the store")
	}
}

// TestGCSStoreListsKeysAndCommonPrefixes: the GCS listing returns the same
// shape, folding the synthetic directory entries into their prefixes.
func TestGCSStoreListsKeysAndCommonPrefixes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("delimiter"); got != "/" {
			t.Errorf("delimiter = %q, want /", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"kind":"storage#objects","items":[{"name":"t/d/r/x/1.log"},{"name":"t/d/r/x/1.e2.log"}],"prefixes":["t/d/r/x/1.log.d/"]}`)
	}))
	defer srv.Close()
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(srv.URL+"/storage/v1/"), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	store := &GCSStore{client: client, bucket: "task-logs"}
	got, err := store.List(context.Background(), "t/d/r/x/1.", "/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sort.Strings(got)
	if fmt.Sprint(got) != fmt.Sprint(wantTryListing) {
		t.Errorf("List = %v, want %v", got, wantTryListing)
	}
}

// TestParseEpochName pins which listed names are logs of the try.
func TestParseEpochName(t *testing.T) {
	cases := map[string]struct {
		epoch int
		ok    bool
	}{
		"1.log": {0, true}, "1.log.d": {0, true}, "1.log.d/": {0, true},
		"1.e7.log": {7, true}, "1.e7.log.d/": {7, true},
		"10.log": {0, false}, "1.e0.log": {0, false}, "1.e07.log": {0, false},
		"1.ex.log": {0, false}, "1.e.log": {0, false}, "1.e7.txt": {0, false}, "2.log": {0, false},
	}
	for name, want := range cases {
		got, ok := parseEpochName(name, 1)
		if ok != want.ok || got != want.epoch {
			t.Errorf("parseEpochName(%q) = %d %v, want %d %v", name, got, ok, want.epoch, want.ok)
		}
	}
}
