package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// DeployDefaults are the build and push settings a cluster advertises to
// `dexaflow deploy` (#1704): what a project, its workspace and its target
// leave unset is taken from here, so a platform team states its registry
// once instead of in every repository. The operator sets them in the
// server's deploy.defaults; GET /api/v2/deployDefaults serves them. None is
// a secret: registry credentials never go here.
type DeployDefaults struct {
	// Registry is the image prefix a project without registry.url pushes
	// to, such as ghcr.io/acme. The image name defaults to the dag_id.
	Registry string `json:"registry" mapstructure:"registry"`
	// Platforms replaces build.platforms for projects that set none.
	Platforms []string `json:"platforms" mapstructure:"platforms"`
	// BaseImage replaces base_image for projects that set none.
	BaseImage string `json:"base_image" mapstructure:"base_image"`
}

// platformPattern is the os/arch[/variant] form a platform takes, the one
// build.ParsePlatforms accepts.
var platformPattern = regexp.MustCompile(`^[A-Za-z0-9]+/[A-Za-z0-9_]+(/[A-Za-z0-9]+)?$`)

// IsEmpty reports whether d sets nothing.
func (d DeployDefaults) IsEmpty() bool {
	return d.Registry == "" && len(d.Platforms) == 0 && d.BaseImage == ""
}

// Validate refuses a value a deploy could not use, naming the field under
// prefix (deploy.defaults on the server). An empty field is valid: it
// leaves the setting to the project and the built-in default.
func (d DeployDefaults) Validate(prefix string) error {
	if err := CheckRegistryPrefix(prefix+".registry", d.Registry); err != nil {
		return err
	}
	for _, p := range d.Platforms {
		if !platformPattern.MatchString(p) {
			return fmt.Errorf("%s.platforms: invalid platform %q: want os/arch or os/arch/variant, such as linux/amd64 or linux/arm64/v8", prefix, p)
		}
	}
	if d.BaseImage != "" {
		if err := CheckImageReference(prefix+".base_image", d.BaseImage); err != nil {
			return err
		}
	}
	return nil
}

// CheckRegistryPrefix refuses a registry prefix (registry.url, a workspace's
// registry) that cannot start an image reference: a scheme, credentials or a
// digest (`@`), a tag on its last path segment, capitals, a trailing slash or
// anything CheckImageReference refuses. A host alone, with or without a port,
// is valid. Empty is valid: no prefix is set.
func CheckRegistryPrefix(field, v string) error {
	if v == "" {
		return nil
	}
	if err := CheckImageReference(field, v); err != nil {
		return err
	}
	switch {
	case strings.Contains(v, "://"):
		return fmt.Errorf("%s %q has a scheme; give the registry prefix without it, such as ghcr.io/acme", field, v)
	case strings.Contains(v, "@"):
		return fmt.Errorf("%s %q contains '@': a registry prefix carries no credentials and no digest", field, v)
	case strings.HasSuffix(v, "/"):
		return fmt.Errorf("%s %q ends with '/'; remove it", field, v)
	case strings.ToLower(v) != v:
		return fmt.Errorf("%s %q has capitals; image references are lowercase", field, v)
	}
	if i := strings.LastIndex(v, "/"); i >= 0 && strings.Contains(v[i+1:], ":") {
		return fmt.Errorf("%s %q ends with a tag; give the prefix only, the deploy adds the image name and tag", field, v)
	}
	return nil
}
