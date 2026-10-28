package build

import (
	"fmt"
	"regexp"
	"strings"
)

// PlatformPattern is the os/arch[/variant] form a platform takes, as a
// regular expression. Case is not checked: docker matches platforms without
// regard to it. domain.DeployDefaults validates cluster platforms with the
// same expression.
const PlatformPattern = `^[A-Za-z0-9]+/[A-Za-z0-9_]+(/[A-Za-z0-9]+)?$`

// platformRe is PlatformPattern compiled.
var platformRe = regexp.MustCompile(PlatformPattern)

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
				return nil, fmt.Errorf("invalid platform %q: want os/arch or os/arch/variant, such as linux/amd64 or linux/arm64/v8", p)
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
