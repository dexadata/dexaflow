package cli

import "io"

// logLineWriter writes task log text, which user code produced, to a
// terminal. It drops control characters (every C0 byte but tab and newline,
// and DEL, so carriage return too), the way the MCP server's stripControl
// does, so a log line cannot move the cursor, retitle the terminal or write
// the clipboard. It starts every line with prefix. It never holds a line
// back, so a line of any length passes through whole.
type logLineWriter struct {
	w      io.Writer
	prefix string
	open   bool // a line was started and has not ended yet
	buf    []byte
}

// Write filters p and writes what remains, prefixing each new line. It
// reports all of p as written, dropped bytes included.
func (l *logLineWriter) Write(p []byte) (int, error) {
	l.buf = l.buf[:0]
	for _, b := range p {
		// C0 bytes and DEL never occur inside a multi-byte UTF-8 sequence, so
		// filtering bytes keeps every printable rune intact.
		printable := b == '\t' || (b >= 0x20 && b != 0x7f)
		if !printable && b != '\n' {
			continue
		}
		if !l.open {
			l.buf = append(l.buf, l.prefix...)
			l.open = true
		}
		l.buf = append(l.buf, b)
		if b == '\n' {
			l.open = false
		}
	}
	if len(l.buf) > 0 {
		if _, err := l.w.Write(l.buf); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// finish ends a last line that had no newline.
func (l *logLineWriter) finish() error {
	if !l.open {
		return nil
	}
	l.open = false
	_, err := io.WriteString(l.w, "\n")
	return err
}
