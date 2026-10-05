package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"sync"
	"time"
)

// ErrObjectNotFound reports that an object-store sink holds no stored log for a
// Ref. Backends translate their own not-found signal (S3 NoSuchKey, GCS
// ErrObjectNotExist, a missing in-memory entry) into this sentinel so the read
// path maps it uniformly.
var ErrObjectNotFound = errors.New("log object not found")

// ObjectStore is the minimal object-store API the ObjectSink needs: write a
// whole object under a key, and read one back. It is deliberately tiny so the
// sink is testable against an in-memory fake, and so any backend can satisfy it
// with its own native SDK — the S3 backend (AWS S3, MinIO, Ceph RGW via
// aws-sdk-go-v2) and the GCS backend (Google Cloud Storage via its native SDK).
// Each provider keeps its own keyless auth path this way. Implementations target
// a single, pre-configured bucket.
type ObjectStore interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
}

// Pruner is implemented by sinks that reclaim their own expired storage: the
// disk sink deletes old files. An object-store sink deliberately does not
// implement it — bucket lifecycle policy owns retention there — so the janitor
// skips pruning for sinks that manage their own lifecycle.
type Pruner interface {
	Prune(now time.Time, retention time.Duration) error
}

// ObjectSink stores each task attempt as a single object in an ObjectStore,
// keyed the same way the disk sink lays out files
// ({prefix}/{tenant}/{dag}/{run}/{task}/{try}.log, or {try}.e{epoch}.log). Object stores have no
// append, so a writer accumulates the attempt's events and rewrites the object
// incrementally (by size and on a time cadence) with a final rewrite on Close,
// so a control plane killed mid-attempt leaves a partial object rather than
// nothing; the live-tail path (a separate Tailer) still streams lines as they
// arrive. It is opt-in — the disk sink stays the default (see NewDurableSink).
type ObjectSink struct {
	ctx    context.Context
	store  ObjectStore
	prefix string
	logger *slog.Logger
}

// NewObjectSink builds an ObjectSink writing to store under an optional key
// prefix. ctx bounds the store operations issued by the writers and readers the
// sink hands out (the Sink interface is context-free by design); pass the
// server's lifecycle context.
//
// logger receives the warnings the write path cannot return — a failed
// incremental flush is retried, not surfaced to the agent, so the log line is
// the ONLY evidence it happened. It is injected rather than taken from
// slog.Default() so the sink honors the handler (format, level, destination) its
// owner configured without depending on that owner having reassigned a
// process-wide global — leoflow-server does call slog.SetDefault, which is what
// covers the call sites nothing injects into, but an embedder need not, and a
// library reaching for its caller's global is the wrong seam either way.
// nil falls back to slog.Default(), deliberately: making the parameter
// mandatory would not pin the production hand-off, it would just move the trap
// from "warnings go somewhere unwatched" to "nil dereference on a flush path
// that only runs while the process is already shutting down". What pins the
// hand-off is a test that reaches this constructor the way the server does, via
// NewDurableSink (#918).
func NewObjectSink(ctx context.Context, store ObjectStore, prefix string, logger *slog.Logger) *ObjectSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &ObjectSink{ctx: ctx, store: store, prefix: prefix, logger: logger}
}

// key maps a Ref to its object key, mirroring DiskSink's on-disk layout so an
// operator can reason about both the same way. path.Join (not filepath.Join)
// keeps forward slashes on every OS, since object keys are not filesystem paths.
func (o *ObjectSink) key(ref Ref) string {
	return path.Join(o.prefix, ref.TenantID, ref.DagID, ref.RunID, ref.TaskID, ref.fileName())
}

// ObjectLister is implemented by object stores that can list keys under a
// prefix. With a delimiter, a key that continues past the delimiter is
// returned once as its common prefix, ending in the delimiter, so a segmented
// attempt lists as one entry. S3Store and GCSStore implement it.
type ObjectLister interface {
	List(ctx context.Context, prefix, delimiter string) ([]string, error)
}

// errNoObjectList reports a store that cannot list.
var errNoObjectList = errors.New("object store cannot list")

// StoredEpochs lists the attempt epochs with a stored log for ref's try, with
// one listing of the {try}. prefix (see EpochLister). A
// store without ObjectLister, or a failed listing (S3 without s3:ListBucket),
// returns an error and the caller probes instead.
func (o *ObjectSink) StoredEpochs(ref Ref) ([]int, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	lister, ok := o.store.(ObjectLister)
	if !ok {
		return nil, errNoObjectList
	}
	dir := path.Join(o.prefix, ref.TenantID, ref.DagID, ref.RunID, ref.TaskID) + "/"
	keys, err := lister.List(o.ctx, dir+fmt.Sprintf("%d.", ref.TryNumber), "/")
	if err != nil {
		o.logger.Debug("listing a try's log objects failed; probing each execution",
			"prefix", dir, "error", err)
		return nil, err
	}
	var epochs []int
	for _, key := range keys {
		if e, ok := parseEpochName(strings.TrimPrefix(key, dir), ref.TryNumber); ok {
			epochs = append(epochs, e)
		}
	}
	return epochs, nil
}

// Open validates the ref and returns a writer that keeps the attempt's object
// current as lines arrive (see objectWriter) and performs the last flush on Close.
func (o *ObjectSink) Open(ref Ref) (LogWriter, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	return newObjectWriter(o.ctx, o.store, o.key(ref), o.logger), nil
}

// Read fetches the stored object for the ref. A missing object surfaces as
// ErrObjectNotFound.
func (o *ObjectSink) Read(ref Ref) (io.ReadCloser, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	rc, err := o.store.Get(o.ctx, o.key(ref))
	if err != nil {
		return nil, fmt.Errorf("reading log object: %w", err)
	}
	return rc, nil
}

// AppendEvent adds one event to the attempt's stored object WITHOUT clobbering
// it. Object stores have no append, so a plain Open+Close would Put a
// marker-only object over the agent's streamed log; instead this reads the
// existing object (tolerating a not-yet-written one), appends the event as a
// JSONL line, and Puts the combined object back (#861).
//
// Read-modify-write with no locking, which is safe because of WHEN it runs, not
// because nothing else can write the key: a live attempt's writer does flush the
// same object incrementally. The only caller is the agent-lost reaper, and what
// separates the two writers is the agent-lost threshold — 90s of heartbeat
// silence (executor.defaultAgentLostThreshold), not "minutes" and not the life
// of the pod. Normally that is enough, because an agent that has stopped
// heartbeating for 90s has stopped streaming too.
//
// The exception, and it IS reachable: heartbeats can be refused while the log
// stream survives. An attempt past max_attempt_credential_lifetime stops getting
// its token renewed, so its heartbeats start failing authentication, while
// StreamLogs — authenticated once at Open and never re-checked — keeps receiving
// lines and flushing them. Agent-lost then fires at the 90s mark, this function
// appends the marker, and the live writer's very next flush Puts its own buffer
// straight over it. Each side Puts the whole object it last read, so the loser
// loses whole flushes, not one line. Anything that shortens the gap between the
// two writers — a lower threshold, a caller that is not the reaper — widens this
// from an exception into the normal case (#918).
func (o *ObjectSink) AppendEvent(ref Ref, ev Event) error {
	if err := ref.validate(); err != nil {
		return err
	}
	key := o.key(ref)
	var buf bytes.Buffer
	rc, err := o.store.Get(o.ctx, key)
	switch {
	case err == nil:
		if _, cerr := io.Copy(&buf, rc); cerr != nil {
			return errors.Join(fmt.Errorf("reading log object for append: %w", cerr), rc.Close())
		}
		if cerr := rc.Close(); cerr != nil {
			return fmt.Errorf("closing log object after read: %w", cerr)
		}
	case errors.Is(err, ErrObjectNotFound):
		// No prior object (agent Put nothing yet): the marker is the whole object.
	default:
		return fmt.Errorf("reading log object for append: %w", err)
	}
	buf.WriteString(EncodeLine(ev) + "\n")
	if err := o.store.Put(o.ctx, key, bytes.NewReader(buf.Bytes())); err != nil {
		return fmt.Errorf("writing appended log object: %w", err)
	}
	return nil
}

// maxBufferedAttemptBytes caps how much of a single task attempt the object sink
// holds in the control plane. Object stores have no append, so every flush
// rewrites the whole object from the accumulated content — the writer must keep
// the entire attempt in memory until Close, and this bound is the per-attempt
// ceiling of that memory (not of the unflushed tail). Without it one chatty task
// could OOM the shared control plane. What the cap no longer decides is
// durability: everything flushed before it trips is already stored. Far above
// any sane task log; it keeps the blast radius of a runaway task to its own
// attempt.
// var (not const) so tests can lower it without buffering 128 MiB.
var maxBufferedAttemptBytes = 128 << 20 // 128 MiB

// Flush cadence of the object writer. A kill loses at most the unflushed tail,
// so these bound the loss; but each flush re-uploads the whole object, so they
// also bound upload amplification. var (not const) so tests can tighten them.
var (
	// objectFlushBytes is the unflushed-tail size that forces a flush, matching
	// the disk sink's bufio threshold so both sinks trail the live log alike.
	objectFlushBytes = 1 << 20 // 1 MiB
	// objectFlushInterval is the cadence on which a writer with anything
	// unflushed rewrites its object, so a quiet task's few lines become durable
	// within seconds instead of at attempt end.
	objectFlushInterval = 5 * time.Second
	// objectFlushDamping delays a flush until the unflushed tail is at least
	// 1/objectFlushDamping of what is already stored. Each flush grows the object
	// by that fraction at minimum, so a log of final size S uploads about
	// (damping+1)*S in total instead of the quadratic bill of rewriting a large
	// object on every tick. Small objects (below damping*objectFlushBytes) are
	// unaffected: their fraction is under the size threshold anyway.
	objectFlushDamping = 8
	// objectFlushMaxStaleness caps how long a non-empty unflushed tail may wait
	// regardless of the damping fraction. The damping bounds upload
	// amplification, never staleness: without this cap a task that logs a large
	// burst and then trickles keeps up to 1/objectFlushDamping of its log
	// unflushed until Close — minutes of an hour-long chatty task, not the
	// seconds the sink promises. Past the cap the tail is flushed; the cost is at
	// most one extra rewrite per quiet cap, after which nothing is unflushed.
	objectFlushMaxStaleness = 60 * time.Second
	// objectPutTimeout bounds each Put. It runs on a context detached from the
	// server's lifecycle, so the final flush on shutdown survives the SIGTERM
	// cancellation yet cannot hang the shutdown forever.
	objectPutTimeout = 30 * time.Second
)

// shouldFlush reports whether an unflushed tail of the given size, over an
// object of `flushed` stored bytes, warrants rewriting the object now: the tail
// reached the size threshold, or the cadence elapsed — in either case only once
// the tail is at least the damping fraction of the stored object — or the tail
// has waited the staleness cap, which overrides the damping.
func shouldFlush(unflushed, flushed int, sinceLast time.Duration) bool {
	if unflushed == 0 {
		return false
	}
	if sinceLast >= objectFlushMaxStaleness {
		return true
	}
	if damping := objectFlushDamping; damping > 0 && unflushed < flushed/damping {
		return false
	}
	return unflushed >= objectFlushBytes || sinceLast >= objectFlushInterval
}

// objectWriter accumulates a task attempt's events in memory and keeps the
// stored object current by rewriting it (overwrite Put of the accumulated
// content) whenever the unflushed tail crosses shouldFlush, from WriteEvent and
// from a flusher goroutine on the time cadence; Close performs the last flush.
// Invariant: after any flush the stored object is a prefix of the attempt, so a
// process kill (no Close) loses at most the unflushed tail. Memory is bounded by
// maxBufferedAttemptBytes; mu serializes the writer against the flusher.
type objectWriter struct {
	ctx    context.Context
	store  ObjectStore
	key    string
	logger *slog.Logger

	mu        sync.Mutex
	buf       bytes.Buffer
	flushed   int  // bytes of buf already stored
	stored    bool // at least one Put succeeded (an empty attempt still Puts once)
	lastFlush time.Time

	stopOnce sync.Once
	stop     chan struct{} // closed by stopFlusher to end the flusher
	done     chan struct{} // closed by the flusher when it exits
}

// newObjectWriter builds a writer and starts its flusher.
func newObjectWriter(ctx context.Context, store ObjectStore, key string, logger *slog.Logger) *objectWriter {
	w := &objectWriter{ctx: ctx, store: store, key: key, logger: logger, lastFlush: time.Now(), stop: make(chan struct{}), done: make(chan struct{})}
	go w.runFlusher(ctx)
	return w
}

// runFlusher rewrites the object on the time cadence while lines are pending, so
// a quiet task's log does not wait for the attempt to end. It exits on Close.
func (w *objectWriter) runFlusher(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(objectFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ticker.C:
			w.mu.Lock()
			w.maybeFlushLocked(ctx)
			w.mu.Unlock()
		}
	}
}

// WriteEvent appends an event to the in-memory buffer as a JSON line, matching
// the JSONL format the disk sink writes and the UI reader decodes, then flushes
// when the tail crosses the threshold. It fails loudly once the attempt exceeds
// maxBufferedAttemptBytes rather than growing the control plane's memory without
// bound; whatever was buffered before the cap is still flushed on Close.
func (w *objectWriter) WriteEvent(ev Event) error {
	line := EncodeLine(ev) + "\n"
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len()+len(line) > maxBufferedAttemptBytes {
		return fmt.Errorf("task attempt log exceeds the %d-byte object-sink buffer cap; not buffering further lines", maxBufferedAttemptBytes)
	}
	if _, err := w.buf.WriteString(line); err != nil {
		return fmt.Errorf("buffering log line: %w", err)
	}
	w.maybeFlushLocked(w.ctx)
	return nil
}

// maybeFlushLocked flushes when shouldFlush says so. An incremental flush
// failure is logged, not returned: the lines stay buffered and the next trigger
// retries, so a transient store error never ends the agent's log stream; Close
// surfaces the final flush's error. Caller holds mu.
func (w *objectWriter) maybeFlushLocked(ctx context.Context) {
	if !shouldFlush(w.buf.Len()-w.flushed, w.flushed, time.Since(w.lastFlush)) {
		return
	}
	if err := w.flushLocked(ctx); err != nil {
		w.logger.Warn("incremental log object flush failed; will retry", "key", w.key, "error", err)
	}
}

// flushLocked rewrites the stored object with the whole accumulated content. The
// Put runs on a context detached from the sink's lifecycle context — which is
// canceled by SIGTERM at exactly the moment the shutdown path closes writers —
// and bounded by objectPutTimeout. Caller holds mu.
func (w *objectWriter) flushLocked(ctx context.Context) error {
	putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), objectPutTimeout)
	defer cancel()
	if err := w.store.Put(putCtx, w.key, bytes.NewReader(w.buf.Bytes())); err != nil {
		return fmt.Errorf("writing log object: %w", err)
	}
	w.flushed = w.buf.Len()
	w.stored = true
	w.lastFlush = time.Now()
	return nil
}

// stopFlusher ends the flusher goroutine and waits for it; idempotent. It is the
// part of Close a process kill never reaches, split out so tests can model the
// kill (flusher gone, no final flush) without leaking the goroutine.
func (w *objectWriter) stopFlusher() {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
}

// Close stops the flusher and performs the final flush — skipped when nothing
// arrived since the last one, so Close is the last flush rather than an extra
// full re-upload. An attempt that logged nothing still stores an empty object.
// Safe to call more than once.
func (w *objectWriter) Close() error {
	w.stopFlusher()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stored && w.flushed == w.buf.Len() {
		return nil
	}
	return w.flushLocked(w.ctx)
}

// NewDurableSink selects the durable log sink from configuration. The default —
// an empty or "disk" backend — returns a DiskSink rooted at dir, so Lite and
// every deployment that does not opt in keep the exact on-disk behavior. The
// "s3" and "gcs" backends return an ObjectSink over store (which the caller
// builds with the matching native SDK from the configured bucket/credentials)
// and require a non-nil store. An unknown backend is rejected rather than
// silently falling back. logger is the process's configured logger, carried to
// the object sink so its retry warnings honor that contract (see
// NewObjectSink); the disk sink ignores it.
func NewDurableSink(ctx context.Context, backend, dir string, store ObjectStore, prefix string, logger *slog.Logger) (Sink, error) {
	switch backend {
	case "", "disk":
		return NewDiskSink(dir), nil
	case "s3", "gcs":
		if store == nil {
			return nil, fmt.Errorf("%s log backend requires an object store", backend)
		}
		return NewObjectSink(ctx, store, prefix, logger), nil
	default:
		return nil, fmt.Errorf("unknown log backend %q (want \"disk\", \"s3\" or \"gcs\")", backend)
	}
}
