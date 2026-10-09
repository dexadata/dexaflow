package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// uiLinks builds web_url links into the Dexaflow UI (#1471). The paths are the
// UI's own routes, so a client never has to know them. With no base URL every
// builder returns "", which keeps the omitempty web_url fields out of results.
type uiLinks struct {
	base string // absolute, no trailing slash; "" turns links off
}

func (l uiLinks) dag(dagID string) string {
	if l.base == "" {
		return ""
	}
	return l.base + "/dags/" + url.PathEscape(dagID)
}

func (l uiLinks) run(dagID, runID string) string {
	if l.base == "" {
		return ""
	}
	return l.dag(dagID) + "/runs/" + url.PathEscape(runID)
}

// task links a task instance; with a try number it opens that attempt.
func (l uiLinks) task(dagID, runID, taskID string, try int) string {
	if l.base == "" {
		return ""
	}
	u := l.run(dagID, runID) + "/tasks/" + url.PathEscape(taskID)
	if try >= 1 {
		u += "?try_number=" + strconv.Itoa(try)
	}
	return u
}

// logLine links one line of a task attempt's log. The UI log viewer scrolls to
// the 0-based line index in the fragment (see uiLogIndexes).
func (l uiLinks) logLine(dagID, runID, taskID string, try, index int) string {
	if l.base == "" {
		return ""
	}
	return l.task(dagID, runID, taskID, try) + "#" + strconv.Itoa(index)
}

// uiLogIndexes maps each raw log line to the index the UI log viewer gives it.
// The viewer drops ::group:: and ::endgroup:: marker lines before numbering, so
// a marker line keeps the index of the next real line.
func uiLogIndexes(lines []string) []int {
	out := make([]int, len(lines))
	n := 0
	for i, ln := range lines {
		out[i] = n
		if !strings.Contains(ln, "::group::") && !strings.Contains(ln, "::endgroup::") {
			n++
		}
	}
	return out
}

// ValidateUIBaseURL checks a UI base URL before it is used: absolute http or
// https, with a host and no query or fragment, since every link appends a path
// to it. Empty is valid and turns links off.
func ValidateUIBaseURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("ui base url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("ui base url %q: want an absolute http or https URL", s)
	}
	if u.Host == "" {
		return fmt.Errorf("ui base url %q: no host", s)
	}
	if u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(s, "?#") {
		return errors.New("ui base url: must not carry a query or fragment")
	}
	return nil
}
