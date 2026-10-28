package domain

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// BuildInfo is where a DAG version was built (#1702): the git commit and
// repository of the sources, the CI pipeline run, the builder that produced
// the image, the base image it was built from and the version of the CLI
// that built and registered it. The base image carries the task agent, so it
// also names the agent version. Every field is optional.
//
// It is metadata of the register, not part of dag.json: the same sources
// built by another pipeline compile to the same dag.json, the same content
// hash and the same version label, and the first register of that content
// keeps its build info. It is the caller's claim, recorded as sent and not
// verified.
type BuildInfo struct {
	Commit      string `json:"commit,omitempty"`
	Repo        string `json:"repo,omitempty"`
	PipelineURL string `json:"pipeline_url,omitempty"`
	Builder     string `json:"builder,omitempty"`
	BaseImage   string `json:"base_image,omitempty"`
	CLIVersion  string `json:"cli_version,omitempty"`
}

// The bounds of each BuildInfo field, in bytes.
const (
	maxBuildURLLen       = 2048
	maxBuildBuilderLen   = 255
	maxBuildBaseImageLen = 512
	maxBuildVersionLen   = 128
)

// buildCommitPattern is a git object id, abbreviated or full (SHA-1 or
// SHA-256), as git prints it: lower case hex.
var buildCommitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// buildInfoField is one field of BuildInfo with its rule.
type buildInfoField struct {
	name  string
	value string
	check func(string) error
}

// fields lists b's fields with the rule each one follows.
func (b BuildInfo) fields() []buildInfoField {
	return []buildInfoField{
		{"commit", b.Commit, checkBuildCommit},
		{"repo", b.Repo, checkBuildURL},
		{"pipeline_url", b.PipelineURL, checkBuildURL},
		{"builder", b.Builder, boundedText(maxBuildBuilderLen, true)},
		{"base_image", b.BaseImage, boundedText(maxBuildBaseImageLen, false)},
		{"cli_version", b.CLIVersion, boundedText(maxBuildVersionLen, false)},
	}
}

// IsZero reports whether b holds no field.
func (b BuildInfo) IsZero() bool {
	return b == BuildInfo{}
}

// Validate returns the first field of b that breaks its rule, named in the
// error: commit is a lower case hex git object id of 7 to 64 characters;
// repo and pipeline_url are absolute http or https URLs without credentials,
// query string or fragment, at most 2048 bytes; every field is valid UTF-8;
// builder (at most 255 bytes), base_image (512) and
// cli_version (128) hold no control or format character, and base_image and
// cli_version no space either. An empty field is always valid.
func (b BuildInfo) Validate() error {
	for _, f := range b.fields() {
		if f.value == "" {
			continue
		}
		if err := f.check(f.value); err != nil {
			return fmt.Errorf("build_info %s: %w", f.name, err)
		}
	}
	return nil
}

// Sanitized returns b without the fields Validate would refuse, so a client
// sends only what a server accepts.
func (b BuildInfo) Sanitized() BuildInfo {
	out := b
	for _, f := range out.fields() {
		if f.value != "" && f.check(f.value) != nil {
			out.clear(f.name)
		}
	}
	return out
}

// clear empties the field called name.
func (b *BuildInfo) clear(name string) {
	switch name {
	case "commit":
		b.Commit = ""
	case "repo":
		b.Repo = ""
	case "pipeline_url":
		b.PipelineURL = ""
	case "builder":
		b.Builder = ""
	case "base_image":
		b.BaseImage = ""
	case "cli_version":
		b.CLIVersion = ""
	}
}

func checkBuildCommit(v string) error {
	if !buildCommitPattern.MatchString(v) {
		return errors.New("must be a git commit id: 7 to 64 lower case hex characters")
	}
	return nil
}

func checkBuildURL(v string) error {
	if err := boundedText(maxBuildURLLen, false)(v); err != nil {
		return err
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an absolute http or https URL")
	}
	if u.User != nil {
		return errors.New("must not carry credentials")
	}
	if strings.ContainsAny(v, "?#") {
		return errors.New("must not carry a query string or a fragment")
	}
	return nil
}

// boundedText is the rule of a free text field: at most limit bytes, no
// control or format character and, unless spaces is set, no white space.
func boundedText(limit int, spaces bool) func(string) error {
	return func(v string) error {
		if len(v) > limit {
			return fmt.Errorf("must be at most %d bytes", limit)
		}
		if !utf8.ValidString(v) {
			return errors.New("must be valid UTF-8")
		}
		for _, r := range v {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				return errors.New("must not contain control or format characters")
			}
			if !spaces && unicode.IsSpace(r) {
				return errors.New("must not contain spaces")
			}
		}
		return nil
	}
}
