package logs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// ReadAttempts reads the log of one try as every execution of it stored,
// oldest attempt epoch first (ADR 0051 amendment, #863). The Airflow-compatible
// log endpoint addresses a try, while an infra re-place, a reschedule poke or a
// repeated dispatch runs the same try again under a new epoch, each writing its
// own stream.
//
// epochs lists the candidate epochs; ref.AttemptEpoch is ignored. A candidate
// with no stored log is skipped (a claimed epoch whose pod never ran wrote
// nothing). A try with exactly one stored stream is returned unchanged, so a
// try that ran once, and every log written before the epoch existed, reads
// byte for byte as before. Several streams are concatenated, each preceded by
// one system line naming the execution. When no candidate has a stored stream
// the sink's own absence error is returned, so the API still answers 404. Any
// other read failure fails the whole read rather than serving a log with a
// silent hole.
func ReadAttempts(sink Sink, ref Ref, epochs []int) (io.ReadCloser, error) {
	type stream struct {
		epoch int
		rc    io.ReadCloser
	}
	var (
		found   []stream
		absence error
	)
	closeAll := func() error {
		errs := make([]error, 0, len(found))
		for _, s := range found {
			errs = append(errs, s.rc.Close())
		}
		return errors.Join(errs...)
	}
	for _, epoch := range uniqueSorted(epochs) {
		r := ref
		r.AttemptEpoch = epoch
		rc, err := sink.Read(r)
		switch {
		case err == nil:
			found = append(found, stream{epoch: epoch, rc: rc})
		case isAbsent(err):
			if absence == nil {
				absence = err
			}
		default:
			return nil, errors.Join(err, closeAll())
		}
	}
	switch len(found) {
	case 0:
		if absence == nil {
			absence = fmt.Errorf("%w: no attempt epochs to read", os.ErrNotExist)
		}
		return nil, absence
	case 1:
		return found[0].rc, nil
	}
	readers := make([]io.Reader, 0, 2*len(found))
	closers := make([]io.Closer, 0, len(found))
	for i, s := range found {
		readers = append(readers, systemLine(ref.TryNumber, s.epoch, i+1, len(found)), &newlineTerminated{r: s.rc})
		closers = append(closers, s.rc)
	}
	return &multiReadCloser{Reader: io.MultiReader(readers...), closers: closers}, nil
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

// systemLine is the JSONL line that introduces one execution's stream in a
// concatenated try log. It carries the zero time: it marks a boundary between
// streams, not an event, so the structured log view renders it without a
// timestamp, and a fixed value keeps the read deterministic. It is marshaled
// directly because EncodeLine stamps a zero time with the current one.
func systemLine(try, epoch, n, total int) io.Reader {
	msg := fmt.Sprintf("execution %d of %d of try %d (attempt epoch %d); a later execution is an infra re-place, a reschedule poke or a redispatch of the same try",
		n, total, try, epoch)
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

// multiReadCloser closes every stream it concatenates.
type multiReadCloser struct {
	io.Reader
	closers []io.Closer
}

// Close closes every underlying stream and joins their errors.
func (m *multiReadCloser) Close() error {
	errs := make([]error, 0, len(m.closers))
	for _, c := range m.closers {
		errs = append(errs, c.Close())
	}
	return errors.Join(errs...)
}
