// Package xcom implements the Dexaflow XCom subsystem: small typed payloads
// passed between tasks, stored in Redis with a hard size limit and a TTL, with
// metadata indexed in Postgres for retrieval (ADR 0006).
package xcom

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// MaxSizeBytes is the hard per-key XCom size limit (256 KB). Larger payloads
// must use a blob store and pass a reference (ADR 0006).
const MaxSizeBytes = 256 * 1024

// DefaultTTL is the default XCom lifetime, configurable per DAG.
const DefaultTTL = 7 * 24 * time.Hour

// Sentinel errors returned by the XCom subsystem.
var (
	// ErrNotFound is returned when a key is absent or expired.
	ErrNotFound = errors.New("xcom not found")
	// ErrTooLarge is returned when a payload exceeds MaxSizeBytes.
	ErrTooLarge = errors.New("xcom payload exceeds the 256KB limit")
	// ErrSchemaMismatch is returned when a payload violates its declared schema.
	ErrSchemaMismatch = errors.New("xcom payload does not match the declared schema")
)

// Key identifies an XCom value. Its string form is the Redis key (ADR 0006):
// xcom:{tenant_id}:{dag_id}:{run_id}:{task_id}:{key_name}.
type Key struct {
	TenantID string
	DagID    string
	RunID    string
	TaskID   string
	Name     string
}

// String returns the canonical Redis key for the XCom value.
func (k Key) String() string {
	return fmt.Sprintf("xcom:%s:%s:%s:%s:%s", k.TenantID, k.DagID, k.RunID, k.TaskID, k.Name)
}

// Entry is a stored XCom value together with its metadata.
type Entry struct {
	Value       []byte    `json:"value"`
	ContentType string    `json:"content_type"`
	SizeBytes   int       `json:"size_bytes"`
	CreatedAt   time.Time `json:"created_at"`
}

// IndexEntry is the Postgres-side metadata recorded for each push, used for UI
// listing and expiry-based cleanup.
type IndexEntry struct {
	TenantID    string
	RunID       string
	TaskID      string
	Name        string
	RedisKey    string
	SizeBytes   int
	ContentType string
	ExpiresAt   time.Time
}

// Backend stores XCom entries with a TTL. RedisBackend is the production
// implementation; tests use a fake.
type Backend interface {
	Push(ctx context.Context, key string, entry Entry, ttl time.Duration) error
	Fetch(ctx context.Context, key string) (Entry, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, pattern string) ([]string, error)
}

// BatchBackend is a Backend that reads several keys in one round trip. found[i]
// is false when keys[i] is absent or expired.
type BatchBackend interface {
	FetchMany(ctx context.Context, keys []string) (entries []Entry, found []bool, err error)
}

// Result is one value of a FetchMany: the entry, and whether it exists.
type Result struct {
	Entry Entry
	Found bool
}

// Index records XCom metadata for retrieval and cleanup (Postgres-backed).
type Index interface {
	RecordXCom(ctx context.Context, entry IndexEntry) error
}

// Service validates and stores XCom values, recording their metadata in the index.
type Service struct {
	backend Backend
	index   Index
	ttl     time.Duration
	now     func() time.Time
}

// NewService builds an XCom service over the given backend and index, applying
// the given TTL to stored values.
func NewService(backend Backend, index Index, ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Service{backend: backend, index: index, ttl: ttl, now: time.Now}
}

// Push validates the payload (size, then schema if non-nil) and stores it,
// recording its metadata in the index.
func (s *Service) Push(ctx context.Context, key Key, value []byte, contentType string, schema map[string]any) error {
	if len(value) > MaxSizeBytes {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(value))
	}
	if schema != nil {
		if err := validateSchema(value, schema); err != nil {
			return fmt.Errorf("%w: %s", ErrSchemaMismatch, err.Error())
		}
	}
	if contentType == "" {
		contentType = "application/json"
	}
	entry := Entry{Value: value, ContentType: contentType, SizeBytes: len(value), CreatedAt: s.now()}
	if err := s.backend.Push(ctx, key.String(), entry, s.ttl); err != nil {
		return fmt.Errorf("storing xcom: %w", err)
	}
	return s.index.RecordXCom(ctx, IndexEntry{
		TenantID:    key.TenantID,
		RunID:       key.RunID,
		TaskID:      key.TaskID,
		Name:        key.Name,
		RedisKey:    key.String(),
		SizeBytes:   len(value),
		ContentType: contentType,
		ExpiresAt:   s.now().Add(s.ttl),
	})
}

// Fetch returns the XCom value for the key, or ErrNotFound if absent/expired.
func (s *Service) Fetch(ctx context.Context, key Key) (Entry, error) {
	return s.backend.Fetch(ctx, key.String())
}

// FetchMany returns the values for keys in the same order, marking absent or
// expired ones as not found. A backend implementing BatchBackend is read in one
// round trip; any other backend is read key by key.
func (s *Service) FetchMany(ctx context.Context, keys []Key) ([]Result, error) {
	out := make([]Result, len(keys))
	if bb, ok := s.backend.(BatchBackend); ok {
		raw := make([]string, len(keys))
		for i, k := range keys {
			raw[i] = k.String()
		}
		entries, found, err := bb.FetchMany(ctx, raw)
		if err != nil {
			return nil, err
		}
		for i := range keys {
			out[i] = Result{Entry: entries[i], Found: found[i]}
		}
		return out, nil
	}
	for i, k := range keys {
		e, err := s.backend.Fetch(ctx, k.String())
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[i] = Result{Entry: e, Found: true}
	}
	return out, nil
}
