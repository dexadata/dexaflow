package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// A server, or a platform in front of it, may add to the register answer and
// to the dry run answer a links object (a name for each URL, such as the
// page of the new version in its own UI) and a checks list (what it checked
// and the result). The engine adds neither; the deploy prints whatever it
// gets, the same way for every server (#1772).

// serverCheck is one entry of a checks list: an object with a name, a status
// and an optional detail, or a bare string, read as the name.
type serverCheck struct {
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// UnmarshalJSON reads a check given as an object or as a bare string.
func (c *serverCheck) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		*c = serverCheck{Name: s}
		return nil
	}
	type plain serverCheck
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("decoding a check: %w", err)
	}
	*c = serverCheck(p)
	return nil
}

// serverExtras are the links and checks of an answer.
type serverExtras struct {
	Links  map[string]string `json:"links,omitempty"`
	Checks []serverCheck     `json:"checks,omitempty"`
}

// readServerExtras decodes the links and checks of an answer body. A body
// whose links or checks are of another shape gives none: they are advisory,
// and the answer itself was read.
func readServerExtras(body []byte) serverExtras {
	var e serverExtras
	if json.Unmarshal(body, &e) != nil {
		return serverExtras{}
	}
	return e
}

// lines words the links, sorted by name, then the checks in the server's
// order, one per line, with every control character the server sent
// removed so it cannot drive the terminal.
func (e serverExtras) lines() string {
	names := make([]string, 0, len(e.Links))
	for name := range e.Links {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "  link     %s: %s\n", printable(name), printable(e.Links[name]))
	}
	for _, c := range e.Checks {
		fmt.Fprintf(&b, "  check    %s\n", c.words())
	}
	return b.String()
}

// words is a check as a line: name: status (detail), each part when present.
func (c serverCheck) words() string {
	s := printable(c.Name)
	if c.Status != "" {
		s += ": " + printable(c.Status)
	}
	if c.Detail != "" {
		s += " (" + printable(c.Detail) + ")"
	}
	return s
}

// printable is s without control characters.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
