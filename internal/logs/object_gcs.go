package logs

import (
	"context"
	"errors"
	"fmt"
	"io"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// GCSConfig configures the Google Cloud Storage object store backing an
// ObjectSink. Auth is keyless-first (ADR 0035): leave CredentialsFile empty to
// use Application Default Credentials, which resolves GKE Workload Identity (or
// the metadata server on a GCE node) with no key material on disk. A credentials
// file is a discouraged escape hatch for local development and clusters without
// an identity broker.
//
// Unlike the S3 backend, GCS needs no region, endpoint, or path-style toggle —
// the native SDK addresses the global storage.googleapis.com API and locates the
// bucket by name. This is why GCS is a first-class provider here rather than an
// S3-interop endpoint: the interop endpoint requires HMAC keys and cannot use
// Workload Identity, so routing GCS through the S3 client would forfeit keyless.
type GCSConfig struct {
	// Bucket is the target bucket. Required.
	Bucket string
	// CredentialsFile is a path to a service-account JSON key. Empty (recommended)
	// uses Application Default Credentials — GKE Workload Identity keyless.
	CredentialsFile string
}

// GCSStore is an ObjectStore backed by Google Cloud Storage.
type GCSStore struct {
	client *storage.Client
	bucket string
}

// NewGCSStore builds a GCSStore from cfg. With no CredentialsFile it constructs
// the client on the keyless ADC chain; with one set it detects credentials from
// that file via the modern auth library (not the deprecated WithCredentialsFile
// option). Client construction may touch the credential chain but makes no bucket
// call; a missing bucket or denied permission surfaces on the first Put/Get.
func NewGCSStore(ctx context.Context, cfg GCSConfig) (*GCSStore, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("gcs log backend requires a bucket")
	}
	var opts []option.ClientOption
	if cfg.CredentialsFile != "" {
		creds, cerr := credentials.DetectDefault(&credentials.DetectOptions{
			CredentialsFile: cfg.CredentialsFile,
			Scopes:          []string{storage.ScopeReadWrite},
		})
		if cerr != nil {
			return nil, fmt.Errorf("loading gcs credentials file: %w", cerr)
		}
		opts = append(opts, option.WithAuthCredentials(creds))
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("creating gcs client: %w", err)
	}
	return &GCSStore{client: client, bucket: cfg.Bucket}, nil
}

// Put writes the object under key. The GCS writer streams to the API, but a task
// attempt's log is already fully buffered by the caller, so io.Copy adds no
// unbounded cost. Close finalizes the object; an error there means the write did
// not commit.
func (g *GCSStore) Put(ctx context.Context, key string, r io.Reader) error {
	w := g.client.Bucket(g.bucket).Object(key).NewWriter(ctx)
	if _, err := io.Copy(w, r); err != nil {
		// Close to release the writer's resources; the copy error is primary, but
		// join a close failure rather than dropping it.
		return fmt.Errorf("putting log object: %w", errors.Join(err, w.Close()))
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finalizing log object: %w", err)
	}
	return nil
}

// Get reads the object under key, translating GCS's ErrObjectNotExist into
// ErrObjectNotFound so the read path maps a missing log to a 404, matching the
// S3 backend.
func (g *GCSStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	r, err := g.client.Bucket(g.bucket).Object(key).NewReader(ctx)
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrObjectNotFound, key)
		}
		return nil, fmt.Errorf("getting log object: %w", err)
	}
	return r, nil
}

// List returns the keys under prefix, and with a delimiter each deeper key's
// common prefix once (see ObjectLister). It needs storage.objects.list.
func (g *GCSStore) List(ctx context.Context, prefix, delimiter string) ([]string, error) {
	q := &storage.Query{Prefix: prefix, Delimiter: delimiter}
	if err := q.SetAttrSelection([]string{"Name"}); err != nil {
		return nil, fmt.Errorf("listing log objects: %w", err)
	}
	it := g.client.Bucket(g.bucket).Objects(ctx, q)
	var out []string
	for {
		attrs, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("listing log objects: %w", err)
		}
		if attrs.Prefix != "" {
			out = append(out, attrs.Prefix)
		} else {
			out = append(out, attrs.Name)
		}
		if len(out) > maxListedKeys {
			return nil, errListTooLong
		}
	}
}
