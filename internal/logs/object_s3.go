package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Config configures the S3-compatible object store backing an ObjectSink. It
// targets AWS S3 and any S3-compatible store (MinIO, Ceph RGW). Google Cloud
// Storage is NOT reached through here — it has its own native backend (GCSStore)
// because the S3 interop endpoint requires HMAC keys and cannot use Workload
// Identity, which would forfeit keyless auth. Keyless is the default (ADR 0035):
// leave AccessKeyID/SecretAccessKey empty to use the ambient credential chain
// (IRSA / instance profile / env). Static keys are a discouraged escape hatch
// for dev and stores without an identity broker.
type S3Config struct {
	// Bucket is the target bucket. Required.
	Bucket string
	// Region is the store region. Required by AWS S3; ignored by some stores.
	Region string
	// Endpoint overrides the S3 endpoint (MinIO, Ceph RGW). Empty = AWS default.
	Endpoint string
	// ForcePathStyle uses path-style addressing (needed by MinIO and some stores).
	ForcePathStyle bool
	// AccessKeyID / SecretAccessKey are static credentials. Empty = keyless.
	AccessKeyID     string
	SecretAccessKey string
}

// S3Store is an ObjectStore backed by an S3-compatible API.
type S3Store struct {
	client *s3.Client
	bucket string
}

// NewS3Store builds an S3Store from cfg. It loads the ambient AWS configuration
// (region + keyless credential chain) and, when set, overrides the endpoint (for
// MinIO / Ceph RGW), forces path-style addressing, and installs static
// credentials. No network call is made here; credential resolution and any
// failure surface on the first Put/Get.
func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("object log backend requires a bucket")
	}
	opts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})
	return &S3Store{client: client, bucket: cfg.Bucket}, nil
}

// Put writes the object under key. Request signing needs a length-known,
// seekable payload; the object sink already hands a *bytes.Reader, so the common
// path passes it straight through with no extra copy. A non-seekable reader is
// materialized once as a fallback so the interface stays honest for other callers.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader) error {
	body, ok := r.(io.ReadSeeker)
	if !ok {
		buf, err := io.ReadAll(r)
		if err != nil {
			return fmt.Errorf("reading log body: %w", err)
		}
		body = bytes.NewReader(buf)
	}
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   body,
	}); err != nil {
		return fmt.Errorf("putting log object: %w", err)
	}
	return nil
}

// Get reads the object under key, translating S3's NoSuchKey into
// ErrObjectNotFound so the read path maps a missing log to a 404.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noKey *s3types.NoSuchKey
		if errors.As(err, &noKey) {
			return nil, fmt.Errorf("%w: %s", ErrObjectNotFound, key)
		}
		return nil, fmt.Errorf("getting log object: %w", err)
	}
	return out.Body, nil
}

// maxListedKeys bounds one List call. A try's listing holds one entry per
// execution (a segmented one lists as its common prefix), so a try past this
// bound is read by probing instead.
const maxListedKeys = 10000

// errListTooLong reports a listing past maxListedKeys.
var errListTooLong = errors.New("object listing exceeds its bound")

// List returns the keys under prefix, and with a delimiter each deeper key's
// common prefix once (see ObjectLister). It needs s3:ListBucket.
func (s *S3Store) List(ctx context.Context, prefix, delimiter string) ([]string, error) {
	in := &s3.ListObjectsV2Input{Bucket: aws.String(s.bucket), Prefix: aws.String(prefix)}
	if delimiter != "" {
		in.Delimiter = aws.String(delimiter)
	}
	var out []string
	p := s3.NewListObjectsV2Paginator(s.client, in)
	for p.HasMorePages() {
		page, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing log objects: %w", err)
		}
		for _, o := range page.Contents {
			out = append(out, aws.ToString(o.Key))
		}
		for _, cp := range page.CommonPrefixes {
			out = append(out, aws.ToString(cp.Prefix))
		}
		if len(out) > maxListedKeys {
			return nil, errListTooLong
		}
	}
	return out, nil
}
