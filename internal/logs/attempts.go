package logs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// TryEpochs names the executions one try log read considers: epoch 0, where
// every log written before the epoch existed lives, and every epoch in
// (Low, High]. Max bounds how many non-zero epochs are served, keeping the most
// recent; 0 means no bound. A reschedule-mode sensor starts a new execution on
// every poke, so a long-running try can span many epochs.
type TryEpochs struct {
	Low, High, Max int
}

// epochSpan is an inclusive range of attempt epochs; the zero value is empty.
type epochSpan struct{ from, to int }

func (s epochSpan) empty() bool { return s == epochSpan{} }

// candidates is the epochs a read probes when the sink cannot list: 0, then
// (Low, High] capped to the most recent Max, with the span the cap dropped. A
// try the database has no row for can have High below Low; that range is
// empty, so only epoch 0 is probed.
func (t TryEpochs) candidates() ([]int, epochSpan) {
	low := max(t.Low, 0)
	if t.High <= low {
		return []int{0}, epochSpan{}
	}
	var dropped epochSpan
	if t.Max > 0 && t.High-low > t.Max {
		dropped = epochSpan{from: low + 1, to: t.High - t.Max}
		low = t.High - t.Max
	}
	out := make([]int, 0, t.High-low+1)
	out = append(out, 0)
	for e := max(low+1, 1); e <= t.High; e++ {
		out = append(out, e)
	}
	return out, dropped
}

// pick keeps the stored epochs the set covers, oldest first, capped to the
// most recent Max non-zero ones, with the span the cap dropped.
func (t TryEpochs) pick(stored []int) ([]int, epochSpan) {
	var nonZero []int
	zero := false
	for _, e := range uniqueSorted(stored) {
		switch {
		case e == 0:
			zero = true
		case e > t.Low && e <= t.High:
			nonZero = append(nonZero, e)
		}
	}
	var dropped epochSpan
	if t.Max > 0 && len(nonZero) > t.Max {
		cut := len(nonZero) - t.Max
		dropped = epochSpan{from: nonZero[0], to: nonZero[cut-1]}
		nonZero = nonZero[cut:]
	}
	if zero {
		return append([]int{0}, nonZero...), dropped
	}
	return nonZero, dropped
}

// EpochLister is implemented by sinks that can name the stored executions of
// one try in a single call (a directory read, or one object listing), so a try
// log read fetches only the streams that exist instead of probing every epoch
// the try can span. StoredEpochs ignores ref.AttemptEpoch. An error means the
// listing is unavailable, and the read falls back to probing.
type EpochLister interface {
	StoredEpochs(ref Ref) ([]int, error)
}

// ReadAttempts reads the log of one try as every execution of it stored,
// oldest attempt epoch first (ADR 0051 amendment, #863). The Airflow-compatible
// log endpoint addresses a try, while an infra re-place, a reschedule poke or a
// repeated dispatch runs the same try again under a new epoch, each writing its
// own stream. ref.AttemptEpoch is ignored.
//
// A sink that implements EpochLister names the stored executions in one call;
// any other sink, or a failed listing, has every candidate epoch probed. A
// candidate with no stored log is skipped (a claimed epoch whose pod never ran
// wrote nothing). A try with exactly one stored stream is returned unchanged,
// so a try that ran once, and every log written before the epoch existed,
// reads byte for byte as before. Several streams are concatenated, each
// preceded by one system line naming the execution, and opened one at a time
// as the read reaches them (without a listing, one stream ahead is open to
// tell a lone stream from several). When set.Max leaves older executions out,
// a first system line says which. When no candidate has a stored stream the
// sink's own absence error is returned, so the API still answers 404. A
// failure to open the first streams fails the call; a later one ends the body
// with that error, so a log is never served with a silent hole.
func ReadAttempts(sink Sink, ref Ref, set TryEpochs) (io.ReadCloser, error) {
	epochs, dropped, listed := selectEpochs(sink, ref, set)
	a := &attemptStreams{sink: sink, ref: ref, pending: epochs}
	if listed {
		a.total = len(epochs)
	}
	first, err := a.openNext()
	if errors.Is(err, errNoStream) {
		return nil, a.absence()
	}
	if err != nil {
		return nil, err
	}
	queue := []piece{}
	if !dropped.empty() {
		queue = append(queue, piece{r: droppedLine(ref.TryNumber, dropped, set.Max)})
	}
	if len(a.pending) == 0 || !listed {
		second, serr := a.openNext()
		if serr != nil && !errors.Is(serr, errNoStream) {
			return nil, errors.Join(serr, first.rc.Close())
		}
		if serr != nil {
			if dropped.empty() {
				return first.rc, nil
			}
			return &lazyConcat{queue: append(queue, piece{r: first.rc, c: first.rc})}, nil
		}
		return &lazyConcat{queue: append(queue, a.framed(first, 1), a.framed(second, 2)), next: a.nextPiece}, nil
	}
	return &lazyConcat{queue: append(queue, a.framed(first, 1)), next: a.nextPiece}, nil
}

// selectEpochs returns the epochs to read, oldest first, the span the bound
// dropped, and whether they come from a listing of the stored executions.
func selectEpochs(sink Sink, ref Ref, set TryEpochs) ([]int, epochSpan, bool) {
	if l, ok := sink.(EpochLister); ok {
		if stored, err := l.StoredEpochs(ref); err == nil {
			epochs, dropped := set.pick(stored)
			if len(epochs) > 0 {
				return epochs, dropped, true
			}
			// Nothing listed: read epoch 0 for the sink's own absence error.
			return []int{0}, epochSpan{}, true
		}
	}
	epochs, dropped := set.candidates()
	return epochs, dropped, false
}

// stream is one opened execution of the try.
type stream struct {
	epoch int
	rc    io.ReadCloser
}

// attemptStreams opens the try's executions in order, skipping absent ones.
type attemptStreams struct {
	sink    Sink
	ref     Ref
	pending []int
	total   int // number of executions when listed, else 0 (unknown)
	opened  int
	absent  error
}

// errNoStream reports that no stored execution is left to open.
var errNoStream = errors.New("no stored execution left")

// openNext opens the next stored execution, or returns errNoStream when none
// is left.
func (a *attemptStreams) openNext() (*stream, error) {
	for len(a.pending) > 0 {
		epoch := a.pending[0]
		a.pending = a.pending[1:]
		r := a.ref
		r.AttemptEpoch = epoch
		rc, err := a.sink.Read(r)
		switch {
		case err == nil:
			a.opened++
			return &stream{epoch: epoch, rc: rc}, nil
		case isAbsent(err):
			if a.absent == nil {
				a.absent = err
			}
		default:
			return nil, err
		}
	}
	return nil, errNoStream
}

// absence is the error for a try with no stored stream.
func (a *attemptStreams) absence() error {
	if a.absent != nil {
		return a.absent
	}
	return fmt.Errorf("%w: no attempt epochs to read", os.ErrNotExist)
}

// framed is one execution's system line followed by its stream.
func (a *attemptStreams) framed(s *stream, n int) piece {
	return piece{r: io.MultiReader(systemLine(a.ref.TryNumber, s.epoch, n, a.total), &newlineTerminated{r: s.rc}), c: s.rc}
}

// nextPiece opens the next execution as the concatenated read reaches it.
func (a *attemptStreams) nextPiece() (piece, error) {
	s, err := a.openNext()
	if errors.Is(err, errNoStream) {
		return piece{}, io.EOF
	}
	if err != nil {
		return piece{}, err
	}
	return a.framed(s, a.opened), nil
}

// isAbsent reports whether a sink read error means "no stored log": the disk
// sink's os.ErrNotExist or an object sink's ErrObjectNotFound.
func isAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrObjectNotFound)
}

// uniqueSorted returns the non-negative epochs in ascending order, once each.
func uniqueSorted(epochs []int) []int {
	seen := make(map[int]bool, len(epochs))
	out := make([]int, 0, len(epochs))
	for _, e := range epochs {
		if e >= 0 && !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Ints(out)
	return out
}

// parseEpochName reads the attempt epoch from the last segment of a stored
// log location of the given try: {try}.log and {try}.log.d are epoch 0,
// {try}.e{epoch}.log and {try}.e{epoch}.log.d carry the epoch. Any other name
// is not a log of the try.
func parseEpochName(name string, try int) (int, bool) {
	rest, ok := strings.CutPrefix(name, strconv.Itoa(try)+".")
	if !ok {
		return 0, false
	}
	rest = strings.TrimSuffix(strings.TrimSuffix(rest, "/"), ".d")
	if rest == "log" {
		return 0, true
	}
	digits, ok := strings.CutSuffix(strings.TrimPrefix(rest, "e"), ".log")
	if !ok || !strings.HasPrefix(rest, "e") || digits == "" {
		return 0, false
	}
	epoch, err := strconv.Atoi(digits)
	if err != nil || epoch <= 0 || strconv.Itoa(epoch) != digits {
		return 0, false
	}
	return epoch, true
}

// droppedLine is the system line that opens a try log read whose bound left
// older executions out.
func droppedLine(try int, dropped epochSpan, maxStreams int) io.Reader {
	msg := fmt.Sprintf("older executions of try %d are not shown (attempt epochs %d to %d): one read serves at most %d executions besides epoch 0",
		try, dropped.from, dropped.to, maxStreams)
	return systemEvent(msg)
}

// systemLine is the JSONL line that introduces one execution's stream in a
// concatenated try log. total is 0 when the number of executions is not known
// up front (the sink could not list them).
func systemLine(try, epoch, n, total int) io.Reader {
	count := fmt.Sprintf("execution %d", n)
	if total > 0 {
		count = fmt.Sprintf("execution %d of %d", n, total)
	}
	return systemEvent(fmt.Sprintf("%s of try %d (attempt epoch %d); a later execution is an infra re-place, a reschedule poke or a redispatch of the same try",
		count, try, epoch))
}

// systemEvent encodes a system line. It carries the zero time: it marks a
// boundary between streams, not an event, so the structured log view renders
// it without a timestamp, and a fixed value keeps the read deterministic. It is
// marshaled directly because EncodeLine stamps a zero time with the current one.
func systemEvent(msg string) io.Reader {
	line, err := json.Marshal(Event{Level: "info", Stream: "system", Message: msg})
	if err != nil {
		line = []byte(msg)
	}
	return strings.NewReader(string(line) + "\n")
}

// newlineTerminated passes r through and adds a final newline when r ended
// without one, so a stream cut mid-line (a writer killed between flushes) is
// not glued to the next execution's system line.
type newlineTerminated struct {
	r    io.Reader
	last byte
	any  bool
	done bool
}

func (n *newlineTerminated) Read(p []byte) (int, error) {
	if n.done {
		return 0, io.EOF
	}
	k, err := n.r.Read(p)
	if k > 0 {
		n.any, n.last = true, p[k-1]
	}
	if errors.Is(err, io.EOF) {
		n.done = true
		if n.any && n.last != '\n' {
			if k < len(p) {
				p[k] = '\n'
				return k + 1, io.EOF
			}
			n.done, n.r = false, strings.NewReader("\n")
			n.any, n.last = false, '\n'
			return k, nil
		}
	}
	return k, err
}

// piece is one part of a concatenated read; c, when set, is closed once the
// part is drained or the read is closed.
type piece struct {
	r io.Reader
	c io.Closer
}

// lazyConcat reads its queued pieces in order, then asks next for more until
// next returns io.EOF. It holds open only the pieces already queued.
type lazyConcat struct {
	queue []piece
	next  func() (piece, error)
	err   error
}

// Read drains the current piece and moves to the next at its end.
func (l *lazyConcat) Read(p []byte) (int, error) {
	for {
		if l.err != nil {
			return 0, l.err
		}
		if len(l.queue) == 0 {
			if l.next == nil {
				return 0, io.EOF
			}
			pc, err := l.next()
			if err != nil {
				l.err = err
				continue
			}
			l.queue = append(l.queue, pc)
		}
		cur := l.queue[0]
		n, err := cur.r.Read(p)
		if !errors.Is(err, io.EOF) {
			return n, err
		}
		l.queue = l.queue[1:]
		if cur.c != nil {
			if cerr := cur.c.Close(); cerr != nil {
				l.err = fmt.Errorf("closing log stream: %w", cerr)
			}
		}
		if n > 0 {
			return n, nil
		}
	}
}

// Close closes every piece still open and joins their errors.
func (l *lazyConcat) Close() error {
	errs := make([]error, 0, len(l.queue))
	for _, pc := range l.queue {
		if pc.c != nil {
			errs = append(errs, pc.c.Close())
		}
	}
	l.queue = nil
	return errors.Join(errs...)
}
