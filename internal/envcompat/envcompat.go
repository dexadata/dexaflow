// Package envcompat keeps the pre-rename LEOFLOW_* environment variables and
// their DEXAFLOW_* names interchangeable.
//
// Every binary calls Mirror before reading its configuration. Afterwards both
// spellings of every variable hold the same value, so code (and older agents,
// task images and scripts) that reads LEOFLOW_* and documentation that says
// DEXAFLOW_* agree. Child processes inherit the mirrored environment.
package envcompat

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// NewPrefix is the environment variable prefix since the Dexaflow rename.
const NewPrefix = "DEXAFLOW_"

// LegacyPrefix is the prefix before the rename. It keeps working.
const LegacyPrefix = "LEOFLOW_"

// Report lists what Mirror found, by legacy variable name.
type Report struct {
	// Legacy holds the variables set only under their LEOFLOW_* name.
	Legacy []string
	// Conflicts holds the variables set under both names with different values.
	Conflicts []string
	// Failed holds the variables Mirror could not write, with the reason.
	Failed []string
}

// Notes renders the report as one human-readable line per finding.
func (r Report) Notes() []string {
	notes := make([]string, 0, len(r.Conflicts)+len(r.Failed)+len(r.Legacy))
	notes = append(notes, r.ConflictNotes()...)
	notes = append(notes, r.Failed...)
	for _, k := range r.Legacy {
		s := strings.TrimPrefix(k, LegacyPrefix)
		notes = append(notes, fmt.Sprintf("%s is read as %s (the variable's current name); both names work", k, NewPrefix+s))
	}
	return notes
}

// ConflictNotes renders one line per conflicting pair.
func (r Report) ConflictNotes() []string {
	notes := make([]string, 0, len(r.Conflicts))
	for _, k := range r.Conflicts {
		s := strings.TrimPrefix(k, LegacyPrefix)
		notes = append(notes, fmt.Sprintf("%s and %s are both set and differ; using %s", k, NewPrefix+s, NewPrefix+s))
	}
	return notes
}

// Mirror makes every NewPrefix variable and its LegacyPrefix counterpart hold
// the same value, writing through set. When both are present and differ, the
// NewPrefix value wins.
func Mirror(environ []string, set func(key, value string) error) Report {
	values := parse(environ)
	var r Report
	for _, s := range suffixes(values) {
		r.mirrorOne(values, s, set)
	}
	return r
}

// MirrorProcess applies Mirror to the current process environment.
func MirrorProcess() Report {
	return Mirror(os.Environ(), os.Setenv)
}

func (r *Report) mirrorOne(values map[string]string, suffix string, set func(key, value string) error) {
	newKey, oldKey := NewPrefix+suffix, LegacyPrefix+suffix
	newVal, hasNew := values[newKey]
	oldVal, hasOld := values[oldKey]
	switch {
	case hasNew && hasOld && newVal != oldVal:
		r.Conflicts = append(r.Conflicts, oldKey)
		r.write(set, oldKey, newVal)
	case hasNew && !hasOld:
		r.write(set, oldKey, newVal)
	case hasOld && !hasNew:
		r.Legacy = append(r.Legacy, oldKey)
		r.write(set, newKey, oldVal)
	}
}

func (r *Report) write(set func(key, value string) error, key, value string) {
	if err := set(key, value); err != nil {
		r.Failed = append(r.Failed, fmt.Sprintf("could not set %s: %v", key, err))
	}
}

func parse(environ []string) map[string]string {
	values := make(map[string]string, len(environ))
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok {
			values[k] = v
		}
	}
	return values
}

// suffixes returns, sorted, the variable names after either prefix.
func suffixes(values map[string]string) []string {
	seen := map[string]struct{}{}
	for k := range values {
		for _, prefix := range []string{NewPrefix, LegacyPrefix} {
			if s, ok := strings.CutPrefix(k, prefix); ok && s != "" {
				seen[s] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
