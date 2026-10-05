package logs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"slices"
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
	layout string
}

// Object layouts. ObjectLayoutSingle (the default) keeps each attempt in one
// object at {try}.log, rewritten on every flush. ObjectLayoutSegmented writes
// the attempt as numbered segments under {try}.log.d/ ({try}.e{epoch}.log.d/
// for a later execution of the try), sealing a segment once it
// reaches objectSegmentBytes, so a flush uploads the open segment instead of the
// whole attempt and the writer holds one segment in memory instead of the whole
// attempt. A server older than the segmented layout reads only {try}.log, which
// is why it is opt-in; every sink reads both layouts regardless of its own.
const (
	ObjectLayoutSingle    = "single"
	ObjectLayoutSegmented = "segmented"
)

// ObjectOption configures an ObjectSink.
type ObjectOption func(*ObjectSink)

// WithObjectLayout selects the layout new attempts are written in. Empty means
// ObjectLayoutSingle; an unknown value is rejected by NewDurableSink.
func WithObjectLayout(layout string) ObjectOption {
	return func(o *ObjectSink) { o.layout = layout }
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
func NewObjectSink(ctx context.Context, store ObjectStore, prefix string, logger *slog.Logger, opts ...ObjectOption) *ObjectSink {
	if logger == nil {
		logger = slog.Default()
	}
	o := &ObjectSink{ctx: ctx, store: store, prefix: prefix, logger: logger}
	for _, opt := range opts {
		opt(o)
	}
	return o
}

// key maps a Ref to its object key, mirroring DiskSink's on-disk layout so an
// operator can reason about both the same way. path.Join (not filepath.Join)
// keeps forward slashes on every OS, since object keys are not filesystem paths.
func (o *ObjectSink) key(ref Ref) string {
	return path.Join(o.prefix, ref.TenantID, ref.DagID, ref.RunID, ref.TaskID, ref.fileName())
}

// segmentKey maps a Ref and a segment number to the segment's object key. The
// segments live in a directory named after the single-layout object plus
// ".d": {try}.log.d for epoch 0, as before the epoch existed, and
// {try}.e{epoch}.log.d otherwise, so each execution of a try keeps its own
// segments (ADR 0051 amendment). The zero-padded number keeps a lexical
// listing in write order.
func (o *ObjectSink) segmentKey(ref Ref, n int) string {
	return path.Join(o.prefix, ref.TenantID, ref.DagID, ref.RunID, ref.TaskID,
		ref.fileName()+".d", fmt.Sprintf("%08d.log", n))
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

// StoredEpochs lists the attempt epochs with a stored log for ref's try, in
// either layout, with one listing of the {try}. prefix (see EpochLister). A
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
			"prefix", logSafe(dir), "error", logSafe(err.Error()))
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
// In the segmented layout the writer starts after any segment already stored for
// the attempt, so a second stream opened once the previous one has closed
// appends instead of overwriting. Two writers live at the same time (a
// reconnect before the old stream ended) can still overwrite each other's
// segments, as they overwrite each other's object in the single layout. A failed
// probe is logged and the writer starts at segment zero.
func (o *ObjectSink) Open(ref Ref) (LogWriter, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	if o.layout != ObjectLayoutSegmented {
		single := o.key(ref)
		return newObjectWriter(o.ctx, o.store, func(int) string { return single }, false, 0, o.logger), nil
	}
	first, err := o.countSegments(ref)
	if err != nil {
		o.logger.Warn("probing stored log segments failed; starting at segment zero",
			"key", logSafe(o.segmentKey(ref, 0)), "error", logSafe(err.Error()))
		first = 0
	}
	keyFn := func(n int) string { return o.segmentKey(ref, n) }
	return newObjectWriter(o.ctx, o.store, keyFn, true, first, o.logger), nil
}

// countSegments returns how many contiguous segments are stored for ref. A
// writer only starts segment n+1 after segment n was stored, so the first
// missing number ends the attempt.
func (o *ObjectSink) countSegments(ref Ref) (int, error) {
	for n := 0; ; n++ {
		rc, err := o.store.Get(o.ctx, o.segmentKey(ref, n))
		if errors.Is(err, ErrObjectNotFound) {
			return n, nil
		}
		if err != nil {
			return 0, err
		}
		if cerr := rc.Close(); cerr != nil {
			return 0, cerr
		}
	}
}

// Read returns the attempt's stored log in either layout: its segments in order
// followed by the single object, which holds the whole log of an attempt written
// in the single layout and only the reaper's markers of a segmented one. A
// missing log surfaces as ErrObjectNotFound.
//
// Each layout looks for its own shape first. The single layout GETs {try}.log
// exactly as it did before segments existed and probes segment zero only when
// that object is missing, so a default deployment pays no extra round trip and
// a store that answers a missing key with something other than not-found (S3
// returns 403 AccessDenied to a caller without s3:ListBucket) cannot fail its
// reads. The price: a segmented attempt that also holds a reaper marker reads
// back as the marker alone once the layout is switched back to single. The
// segmented layout probes segment zero first and falls back to {try}.log when
// that probe fails for any reason.
func (o *ObjectSink) Read(ref Ref) (io.ReadCloser, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	if o.layout != ObjectLayoutSegmented {
		rc, err := o.store.Get(o.ctx, o.key(ref))
		if err == nil {
			return rc, nil
		}
		if !errors.Is(err, ErrObjectNotFound) {
			return nil, fmt.Errorf("reading log object: %w", err)
		}
		first, serr := o.store.Get(o.ctx, o.segmentKey(ref, 0))
		if serr != nil {
			if !errors.Is(serr, ErrObjectNotFound) {
				o.logger.Debug("probing log segment failed; treating the log as missing",
					"key", logSafe(o.segmentKey(ref, 0)), "error", logSafe(serr.Error()))
			}
			return nil, fmt.Errorf("reading log object: %w", err)
		}
		return &segmentReader{sink: o, ref: ref, cur: first, next: 1}, nil
	}
	first, err := o.store.Get(o.ctx, o.segmentKey(ref, 0))
	switch {
	case err == nil:
		return &segmentReader{sink: o, ref: ref, cur: first, next: 1}, nil
	case !errors.Is(err, ErrObjectNotFound):
		o.logger.Warn("probing log segment failed; reading the single object",
			"key", logSafe(o.segmentKey(ref, 0)), "error", logSafe(err.Error()))
	}
	rc, err := o.store.Get(o.ctx, o.key(ref))
	if err != nil {
		return nil, fmt.Errorf("reading log object: %w", err)
	}
	return rc, nil
}

// segmentReader streams a segmented attempt: each segment is fetched only once
// the previous one is drained, then the single object (the reaper's markers) if
// one exists.
type segmentReader struct {
	sink   *ObjectSink
	ref    Ref
	cur    io.ReadCloser
	next   int
	tailed bool // the single object was already fetched or found missing
}

// Read drains the current object and moves on to the next one at its end.
func (r *segmentReader) Read(p []byte) (int, error) {
	for r.cur != nil {
		n, err := r.cur.Read(p)
		if !errors.Is(err, io.EOF) {
			return n, err
		}
		cerr := r.cur.Close()
		r.cur = nil
		if cerr != nil {
			return n, fmt.Errorf("closing log segment: %w", cerr)
		}
		if aerr := r.advance(); aerr != nil {
			return n, aerr
		}
		if n > 0 {
			return n, nil
		}
	}
	return 0, io.EOF
}

// advance opens the next segment, or the single object once the segments run
// out, leaving cur nil when nothing is left.
func (r *segmentReader) advance() error {
	if r.tailed {
		return nil
	}
	rc, err := r.sink.store.Get(r.sink.ctx, r.sink.segmentKey(r.ref, r.next))
	if err == nil {
		r.cur, r.next = rc, r.next+1
		return nil
	}
	if !errors.Is(err, ErrObjectNotFound) {
		return fmt.Errorf("reading log segment: %w", err)
	}
	r.tailed = true
	rc, err = r.sink.store.Get(r.sink.ctx, r.sink.key(r.ref))
	switch {
	case err == nil:
		r.cur = rc
	case !errors.Is(err, ErrObjectNotFound):
		return fmt.Errorf("reading log object: %w", err)
	}
	return nil
}

// Close releases the object currently being read, if any.
func (r *segmentReader) Close() error {
	if r.cur == nil {
		return nil
	}
	err := r.cur.Close()
	r.cur = nil
	return err
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
// from an exception into the normal case (#918). In the segmented layout the
// live writer never writes {try}.log, so the marker there cannot be overwritten;
// Read serves it after the segments.
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
// attempt. In the segmented layout the writer only holds the open segment and
// its unflushed tail, but the cap still counts the sealed segments, so it stays
// the stored-size ceiling of an attempt in both layouts.
// var (not const) so tests can lower it without buffering 128 MiB.
var maxBufferedAttemptBytes = 128 << 20 // 128 MiB

// Flush cadence of the object writer. A kill loses at most the unflushed tail,
// so these bound the loss; but each flush re-uploads the whole object (the open
// segment in the segmented layout), so they also bound upload amplification. var (not const) so tests can tighten them.
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
	// objectSegmentBytes is the size at which the segmented layout seals the open
	// segment and starts the next one. It bounds both the bytes a flush uploads
	// and the writer's memory, at the cost of one Get per segment on read.
	objectSegmentBytes = 4 << 20 // 4 MiB
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
// maxBufferedAttemptBytes.
//
// In the segmented layout the object being rewritten is the open segment: once a
// flush stores a segment of at least objectSegmentBytes it is sealed, dropped
// from memory, and later lines go to the next number. Segment n+1 is only
// started after segment n was stored, so the stored segments are always
// contiguous and a reader stops at the first missing number.
//
// Two locks keep the network out of the buffer's critical section: mu guards the
// buffer and is only held to append or to snapshot it, while flushMu serializes
// the Puts. A flush copies nothing: lines are only ever appended, so the bytes
// of a snapshot are never written again while its Put is in flight.
type objectWriter struct {
	ctx       context.Context
	store     ObjectStore
	keyFn     func(segment int) string
	segmented bool
	logger    *slog.Logger

	flushMu sync.Mutex // held for the duration of a Put; taken before mu

	mu        sync.Mutex
	buf       []byte // the attempt (single layout) or the open segment and its tail
	flushed   int    // bytes of buf already stored
	stored    bool   // at least one Put succeeded (an empty attempt still Puts once)
	lastFlush time.Time
	segment   int // number of the open segment; always 0 in the single layout
	sealed    int // bytes of the segments this writer sealed; counted against the attempt cap

	stopOnce sync.Once
	stop     chan struct{} // closed by stopFlusher to end the flusher
	done     chan struct{} // closed by the flusher when it exits
}

// newObjectWriter builds a writer and starts its flusher. keyFn maps a segment
// number to its key; the single layout ignores the number.
func newObjectWriter(ctx context.Context, store ObjectStore, keyFn func(int) string, segmented bool, firstSegment int, logger *slog.Logger) *objectWriter {
	w := &objectWriter{
		ctx: ctx, store: store, keyFn: keyFn, segmented: segmented, segment: firstSegment, logger: logger,
		lastFlush: time.Now(), stop: make(chan struct{}), done: make(chan struct{}),
	}
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
			w.flushMu.Lock()
			w.maybeFlush(ctx)
			w.flushMu.Unlock()
		}
	}
}

// WriteEvent appends an event to the in-memory buffer as a JSON line, matching
// the JSONL format the disk sink writes and the UI reader decodes, then flushes
// when the tail crosses the threshold. It fails loudly once the buffer would
// exceed maxBufferedAttemptBytes rather than growing the control plane's memory
// without bound; whatever was buffered before the cap is still flushed on Close.
// When the flusher already has a Put in flight the write does not wait for it:
// the next trigger picks the tail up.
func (w *objectWriter) WriteEvent(ev Event) error {
	return w.WriteLine(EncodeLine(ev))
}

// WriteLine buffers a line already encoded by EncodeLine, so a caller that also
// publishes the line encodes it once. Same cap and flush rules as WriteEvent.
func (w *objectWriter) WriteLine(line string) error {
	w.mu.Lock()
	n := len(line) + 1 // the line plus its newline
	if w.sealed+len(w.buf)+n > maxBufferedAttemptBytes {
		w.mu.Unlock()
		return fmt.Errorf("task attempt log exceeds the %d-byte object-sink buffer cap; not buffering further lines", maxBufferedAttemptBytes)
	}
	if cap(w.buf)-len(w.buf) < n {
		// Double rather than append's gentler growth for large slices: an attempt's
		// buffer grows to megabytes and every regrowth copies it.
		w.buf = slices.Grow(w.buf, max(n, len(w.buf)))
	}
	w.buf = append(w.buf, line...)
	w.buf = append(w.buf, '\n')
	due := w.dueLocked()
	w.mu.Unlock()
	if due && w.flushMu.TryLock() {
		w.maybeFlush(w.ctx)
		w.flushMu.Unlock()
	}
	return nil
}

// dueLocked reports whether shouldFlush calls for a flush now. Caller holds mu.
func (w *objectWriter) dueLocked() bool {
	return shouldFlush(len(w.buf)-w.flushed, w.flushed, time.Since(w.lastFlush))
}

// maybeFlush flushes when shouldFlush says so. An incremental flush failure is
// logged, not returned: the lines stay buffered and the next trigger retries, so
// a transient store error never ends the agent's log stream; Close surfaces the
// final flush's error. Caller holds flushMu.
func (w *objectWriter) maybeFlush(ctx context.Context) {
	w.mu.Lock()
	due := w.dueLocked()
	w.mu.Unlock()
	if !due {
		return
	}
	if err := w.flush(ctx); err != nil {
		w.logger.Warn("incremental log object flush failed; will retry", "key", logSafe(w.currentKey()), "error", logSafe(err.Error()))
	}
}

// currentKey is the key the next flush writes.
func (w *objectWriter) currentKey() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.keyFn(w.segment)
}

// flush stores a snapshot of the buffer: the whole attempt in the single layout,
// the open segment in the segmented one. Only the snapshot is taken under mu; the
// Put runs without it, on a context detached from the sink's lifecycle context
// (which is canceled by SIGTERM at exactly the moment the shutdown path closes
// writers) and bounded by objectPutTimeout. Caller holds flushMu.
func (w *objectWriter) flush(ctx context.Context) error {
	w.mu.Lock()
	n := len(w.buf)
	body := w.buf[:n:n]
	key := w.keyFn(w.segment)
	w.mu.Unlock()

	putCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), objectPutTimeout)
	defer cancel()
	if err := w.store.Put(putCtx, key, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("writing log object: %w", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushed, w.stored, w.lastFlush = n, true, time.Now()
	if w.segmented && n >= objectSegmentBytes {
		// Seal: keep only what arrived during the Put, in a fresh array. The old
		// one is not reused: an earlier Put that timed out may still have its
		// body read by the transport. The new array is sized for about one
		// segment, never for whatever a burst during a slow Put grew the old one
		// to, so later segments do not inherit that capacity.
		w.buf = append(make([]byte, 0, max(len(w.buf)-n, min(cap(w.buf), 2*objectSegmentBytes))), w.buf[n:]...)
		w.flushed = 0
		w.sealed += n
		w.segment++
	}
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
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	w.mu.Lock()
	idle := w.stored && w.flushed == len(w.buf)
	w.mu.Unlock()
	if idle {
		return nil
	}
	return w.flush(w.ctx)
}

// NewDurableSink selects the durable log sink from configuration. The default —
// an empty or "disk" backend — returns a DiskSink rooted at dir, so Lite and
// every deployment that does not opt in keep the exact on-disk behavior. The
// "s3" and "gcs" backends return an ObjectSink over store (which the caller
// builds with the matching native SDK from the configured bucket/credentials)
// and require a non-nil store. An unknown backend is rejected rather than
// silently falling back. logger is the process's configured logger, carried to
// the object sink so its retry warnings honor that contract (see
// NewObjectSink); the disk sink ignores it, and so it does opts. An unknown
// object layout is rejected like an unknown backend.
func NewDurableSink(ctx context.Context, backend, dir string, store ObjectStore, prefix string, logger *slog.Logger, opts ...ObjectOption) (Sink, error) {
	switch backend {
	case "", "disk":
		return NewDiskSink(dir), nil
	case "s3", "gcs":
		if store == nil {
			return nil, fmt.Errorf("%s log backend requires an object store", backend)
		}
		sink := NewObjectSink(ctx, store, prefix, logger, opts...)
		switch sink.layout {
		case "", ObjectLayoutSingle, ObjectLayoutSegmented:
			return sink, nil
		default:
			return nil, fmt.Errorf("unknown object log layout %q (want %q or %q)", sink.layout, ObjectLayoutSingle, ObjectLayoutSegmented)
		}
	default:
		return nil, fmt.Errorf("unknown log backend %q (want \"disk\", \"s3\" or \"gcs\")", backend)
	}
}
