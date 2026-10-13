package cli

import (
	"path"
	"regexp"
	"strings"
)

// dockerignore is a .dockerignore read the way the builder reads it, enough
// for the version label to leave out what never reaches the build context.
// It follows the rules of Docker's pattern matcher (moby/patternmatcher, not a
// dependency of this module): patterns are anchored at the context root after
// path.Clean, `*` and `?` stop at a slash, `**` spans any number of
// directories, a pattern that matches a directory excludes everything under
// it, and the last matching line wins, so `!` re-includes.
type dockerignore struct {
	rules     []ignoreRule
	negations []string // the cleaned patterns of the `!` lines
}

type ignoreRule struct {
	re     *regexp.Regexp
	negate bool
}

// parseDockerignore reads .dockerignore content. A line that is blank or a
// comment is skipped; a pattern that does not compile matches nothing.
func parseDockerignore(data []byte) dockerignore {
	var d dockerignore
	for _, line := range strings.Split(string(data), "\n") {
		p := strings.TrimSpace(line)
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		negate := strings.HasPrefix(p, "!")
		if negate {
			p = strings.TrimSpace(p[1:])
		}
		p = strings.TrimPrefix(path.Clean("/"+p), "/")
		if p == "" {
			continue
		}
		re, err := regexp.Compile(ignorePatternRegexp(p))
		if err != nil {
			continue
		}
		d.rules = append(d.rules, ignoreRule{re: re, negate: negate})
		if negate {
			d.negations = append(d.negations, p)
		}
	}
	return d
}

// ignorePatternRegexp translates one cleaned pattern into an anchored
// regular expression.
func ignorePatternRegexp(p string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c == '*' && i+1 < len(p) && p[i+1] == '*':
			i++
			if i+1 < len(p) && p[i+1] == '/' {
				i++
				b.WriteString("(.*/)?")
			} else {
				b.WriteString(".*")
			}
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(p[i:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			b.WriteString(charClass(p[i+1 : i+end]))
			i += end
		case c == '\\' && i+1 < len(p):
			// An escaped character is taken literally; this is all the
			// escaping the label needs, as patterns rarely carry one.
			i++
			b.WriteString(regexp.QuoteMeta(string(p[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// charClass renders the inside of a `[...]` class so it never matches a
// slash, as in Docker's matcher: a negated class (`^` or `!`) also excludes
// '/', and a plain one drops a literal '/'. A range spanning '/' (such as
// `!-0`) is left as written.
func charClass(inner string) string {
	if strings.HasPrefix(inner, "^") || strings.HasPrefix(inner, "!") {
		return "[^/" + regexpClassBody(inner[1:]) + "]"
	}
	return "[" + regexpClassBody(strings.ReplaceAll(inner, "/", "")) + "]"
}

// regexpClassBody escapes the one character a glob class may carry that
// opens a nested class in a regular expression.
func regexpClassBody(s string) string {
	return strings.ReplaceAll(s, "[", `\[`)
}

// mayReinclude reports whether a `!` line could bring back something under
// the excluded directory dir, so the walk has to descend into it, as Docker's
// walker does: the negation has more segments than dir and its leading
// segments match dir's segment by segment (`!*/keep.txt` reaches data/), or a
// `**` segment comes first, which can match anywhere.
func (d dockerignore) mayReinclude(dir string) bool {
	dirSegs := strings.Split(dir, "/")
	for _, p := range d.negations {
		if negationReaches(strings.Split(p, "/"), dirSegs) {
			return true
		}
	}
	return false
}

func negationReaches(patSegs, dirSegs []string) bool {
	for i, ds := range dirSegs {
		if i >= len(patSegs) {
			return false
		}
		if patSegs[i] == "**" {
			return true
		}
		if ok, _ := path.Match(patSegs[i], ds); !ok { //nolint:errcheck // a malformed segment matches nothing
			return false
		}
	}
	return len(patSegs) > len(dirSegs)
}

// excluded reports whether the slash path relative to the context root stays
// out of the build context: the last rule matching the path or one of its
// parent directories decides.
func (d dockerignore) excluded(slash string) bool {
	out := false
	for _, r := range d.rules {
		if r.matchesOrParent(slash) {
			out = !r.negate
		}
	}
	return out
}

func (r ignoreRule) matchesOrParent(slash string) bool {
	for p := slash; ; {
		if r.re.MatchString(p) {
			return true
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			return false
		}
		p = p[:i]
	}
}
