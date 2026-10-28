package api

import (
	"errors"
	"net/url"
	"regexp"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// promotedTargetPattern is what promoted_from may hold: a target name as the
// CLI accepts one (`dexaflow target add`), so the audit entry never carries
// free text, control or bidi characters.
var promotedTargetPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// maxPromotedServerLen bounds promoted_from_server, a URL.
const maxPromotedServerLen = 2048

// promotedQuery reads the optional provenance of a promoted version (#1708):
// promoted_from, the source target's name, and promoted_from_server, the
// source control plane's URL, which needs promoted_from. Both absent is nil.
// The values are the caller's claim, recorded as sent and not verified. A
// name outside promotedTargetPattern, or a server that is not an absolute
// http(s) URL without credentials and with no control or format character,
// is an error.
func promotedQuery(c *gin.Context) (*domain.DagPromotion, error) {
	target, hasTarget := c.GetQuery("promoted_from")
	server, hasServer := c.GetQuery("promoted_from_server")
	if !hasTarget && !hasServer {
		return nil, nil //nolint:nilnil // absent is a register that is not a promotion
	}
	if !hasTarget {
		return nil, errors.New("promoted_from_server needs promoted_from")
	}
	if !promotedTargetPattern.MatchString(target) {
		return nil, errors.New("promoted_from must be a target name: lower case letters, digits, _ and -, starting with a letter or digit, at most 63 characters")
	}
	if hasServer {
		if err := checkPromotedServer(server); err != nil {
			return nil, err
		}
	}
	return &domain.DagPromotion{Target: target, Server: server}, nil
}

// checkPromotedServer refuses a promoted_from_server that is too long, holds
// a control or format character, is not an absolute http or https URL with
// a host, or carries credentials.
func checkPromotedServer(v string) error {
	if v == "" || len(v) > maxPromotedServerLen {
		return errors.New("promoted_from_server must be a URL of 1 to 2048 bytes")
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return errors.New("promoted_from_server must not contain control or format characters")
		}
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("promoted_from_server must be an absolute http or https URL")
	}
	if u.User != nil {
		return errors.New("promoted_from_server must not carry credentials")
	}
	return nil
}
