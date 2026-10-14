package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// buildInfoParams are the query parameters of a register that carry where
// the version was built (#1702), each with the BuildInfo field it sets.
var buildInfoParams = []struct {
	name string
	set  func(*domain.BuildInfo, string)
}{
	{"build_commit", func(b *domain.BuildInfo, v string) { b.Commit = v }},
	{"build_repo", func(b *domain.BuildInfo, v string) { b.Repo = v }},
	{"build_pipeline_url", func(b *domain.BuildInfo, v string) { b.PipelineURL = v }},
	{"build_builder", func(b *domain.BuildInfo, v string) { b.Builder = v }},
	{"build_base_image", func(b *domain.BuildInfo, v string) { b.BaseImage = v }},
	{"build_cli_version", func(b *domain.BuildInfo, v string) { b.CLIVersion = v }},
}

// buildInfoQuery reads the optional build_* query parameters of a register:
// nil when none is sent. Build info is metadata of the register, not part of
// dag.json, so it never changes the spec, its hash or the version label. It
// is the caller's claim, external input checked by domain.BuildInfo.Validate;
// a parameter sent empty or breaking its rule is refused, named in the error.
func buildInfoQuery(c *gin.Context) (*domain.BuildInfo, error) {
	var out domain.BuildInfo
	sent := false
	for _, p := range buildInfoParams {
		raw, ok := c.GetQuery(p.name)
		if !ok {
			continue
		}
		if raw == "" {
			return nil, errors.New(p.name + " must not be empty")
		}
		var one domain.BuildInfo
		p.set(&one, raw)
		if err := one.Validate(); err != nil {
			return nil, errors.New(p.name + ": " + fieldRule(err))
		}
		p.set(&out, raw)
		sent = true
	}
	if !sent {
		return nil, nil //nolint:nilnil // absent is a register that sent no build info
	}
	return &out, nil
}

// fieldRule is the rule part of a BuildInfo.Validate error ("build_info
// <field>: <rule>"), so the refusal names the query parameter instead.
func fieldRule(err error) string {
	msg := err.Error()
	if _, rule, ok := strings.Cut(msg, ": "); ok {
		return rule
	}
	return msg
}
