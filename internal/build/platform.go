package build

import (
	"fmt"
	"regexp"
	"strings"
)

// platformRe is the os/arch[/variant] form a platform takes, lowercase.
var platformRe = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+(/[a-z0-9]+)?$`)

// ParsePlatforms validates platform values as given on a command line, each
// either one platform or a comma-separated list, and returns them in order
// with duplicates removed. An empty or malformed value is an error: the value
// reaches a build tool's argv and a registry manifest, so only the os/arch
// form is accepted.
func ParsePlatforms(values []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, v := range values {
		for _, p := range strings.Split(v, ",") {
			p = strings.TrimSpace(p)
			if !platformRe.MatchString(p) {
				return nil, fmt.Errorf("invalid platform %q: want os/arch or os/arch/variant in lowercase, such as linux/amd64 or linux/arm64/v8", p)
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no platform given: want os/arch, such as linux/amd64")
	}
	return out, nil
}
