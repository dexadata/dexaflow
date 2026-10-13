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
	rules       []ignoreRule
	hasNegation bool
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
		d.hasNegation = d.hasNegation || negate
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
			b.WriteString(p[i : i+end+1])
			i += end
		case c == '\\' && i+1 < len(p):
			i++
			b.WriteString(regexp.QuoteMeta(string(p[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
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
