package api

import (
	"errors"
	"net/url"
	"unicode"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// maxPromotedFromLen bounds promoted_from and promoted_from_server: they are
// recorded in an audit entry, never a free-form payload.
const maxPromotedFromLen = 128

// promotedQuery reads the optional provenance of a promoted version (#1708):
// promoted_from, the source target's name, and promoted_from_server, the
// source control plane's URL, which needs promoted_from. Both absent is nil.
// A value that is empty, too long, holds a control character, or a server
// that is not an absolute http(s) URL without credentials is an error.
func promotedQuery(c *gin.Context) (*domain.DagPromotion, error) {
	target, hasTarget := c.GetQuery("promoted_from")
	server, hasServer := c.GetQuery("promoted_from_server")
	if !hasTarget && !hasServer {
		return nil, nil //nolint:nilnil // absent is a register that is not a promotion
	}
	if !hasTarget {
		return nil, errors.New("promoted_from_server needs promoted_from")
	}
	if err := checkPromotedValue("promoted_from", target); err != nil {
		return nil, err
	}
	if hasServer {
		if err := checkPromotedServer(server); err != nil {
			return nil, err
		}
	}
	return &domain.DagPromotion{Target: target, Server: server}, nil
}

// checkPromotedValue refuses an empty or over-long value, or one with a
// control character.
func checkPromotedValue(name, v string) error {
	if v == "" {
		return errors.New(name + " must not be empty")
	}
	if len(v) > maxPromotedFromLen {
		return errors.New(name + " is longer than 128 bytes")
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return errors.New(name + " must not contain control characters")
		}
	}
	return nil
}

// checkPromotedServer refuses a promoted_from_server that is not an absolute
// http or https URL with a host, or that carries credentials.
func checkPromotedServer(v string) error {
	if err := checkPromotedValue("promoted_from_server", v); err != nil {
		return err
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
