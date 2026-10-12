package domain

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// ErrInvalidDbtProject reports a dbt.project path the compiler cannot use.
var ErrInvalidDbtProject = errors.New("invalid dbt.project")

// ErrInvalidBuildDockerfile reports a build.dockerfile path outside the project.
var ErrInvalidBuildDockerfile = errors.New("invalid build.dockerfile")

// validateDbtProject rejects a dbt.project path that cannot resolve.
//
// The value is used two ways, and both assume a relative path inside the DAG
// directory: dbtProjectDir resolves it with filepath.Join(dagDir, project), and a
// Pro image build bakes the project at that same relative path inside the image.
//
// filepath.Join does not treat an absolute second element specially, so
// Join("/dags/sales", "/opt/dbt/proj") is "/dags/sales/opt/dbt/proj" — the
// leading slash is swallowed and dbt is pointed at a directory nobody named.
// A path escaping upward resolves outside the DAG directory, and therefore
// outside the Docker build context, so the image cannot contain it either.
//
// Both failures are silent at compile: the path is a string until dbt is invoked
// inside a pod, where it surfaces as "project directory does not exist" with no
// indication that dexaflow.yaml was the cause.
func (c *LeoflowConfig) validateDbtProject() error {
	// Both fields feed the same filepath.Join chain: project is joined onto the
	// DAG directory, then manifest is joined onto that result. Validating only
	// project would leave half the defect in place.
	fields := make([]struct{ field, value string }, 0, 2+2*len(c.DbtGroups))
	if c.Dbt != nil {
		fields = append(fields,
			struct{ field, value string }{"dbt.project", c.Dbt.Project},
			struct{ field, value string }{"dbt.manifest", c.Dbt.Manifest},
		)
	}
	// dbt_groups (ADR 0043) reach the same Join chain and the same build
	// context, and this guard used to return early whenever cfg.Dbt was nil —
	// which is every hybrid DAG, the mode leoflow actually targets. Sorted
	// rather than ranged so a config with several bad groups always names the
	// same one: DbtGroups is a map, and an error message that changes between
	// identical compiles is its own bug.
	for _, name := range slices.Sorted(maps.Keys(c.DbtGroups)) {
		group := c.DbtGroups[name]
		if group == nil {
			continue
		}
		fields = append(fields,
			struct{ field, value string }{"dbt_groups." + name + ".project", group.Project},
			struct{ field, value string }{"dbt_groups." + name + ".manifest", group.Manifest},
		)
	}
	for _, f := range fields {
		if err := containedRelativePath(f.field, f.value); err != nil {
			return err
		}
	}
	return nil
}

// containedRelativePath rejects a path that filepath.Join would mangle or that
// would land outside the DAG directory. An empty value means "not declared".
func containedRelativePath(field, value string) error {
	switch clean, problem := pathOutsideProject(value); problem {
	case pathIsAbsolute:
		return fmt.Errorf("%w: %s %q is absolute; it must be relative to the directory holding dexaflow.yaml, "+
			"because a Pro image build bakes the project at that relative path inside the image",
			ErrInvalidDbtProject, field, value)
	case pathEscapes:
		return fmt.Errorf("%w: %s %q resolves to %q, outside the directory holding dexaflow.yaml; "+
			"the Docker build context cannot reach it, so it would be missing from the image",
			ErrInvalidDbtProject, field, value, clean)
	case pathContained:
	}
	return nil
}

// validateBuildDockerfile refuses a build.dockerfile outside the project
// (#1272). The value was joined onto the project directory as written, so
// `../../other/Dockerfile` built from a file nobody reviewed with this
// dexaflow.yaml, and skipped every check the generated Dockerfile gets: it is
// the one way to bypass those checks that the docs describe. The same shapes are
// refused here as in include_paths and dbt.project. A symlink that leads out of
// the project is lexically clean, so `compile --build` resolves the path again
// against the real project directory before it builds.
func (c *LeoflowConfig) validateBuildDockerfile() error {
	if c.Build == nil {
		return nil
	}
	value := c.Build.Dockerfile
	switch clean, problem := pathOutsideProject(value); problem {
	case pathIsAbsolute:
		return fmt.Errorf("%w: build.dockerfile %q is absolute; it must be relative to the directory holding dexaflow.yaml",
			ErrInvalidBuildDockerfile, value)
	case pathEscapes:
		return fmt.Errorf("%w: build.dockerfile %q resolves to %q, outside the directory holding dexaflow.yaml; "+
			"keep the Dockerfile inside the project so it is reviewed with it",
			ErrInvalidBuildDockerfile, value, clean)
	case pathContained:
	}
	return nil
}

// pathProblem says why a project-relative path is unusable, or that it is fine.
type pathProblem int

const (
	pathContained pathProblem = iota
	pathIsAbsolute
	pathEscapes
)

// pathOutsideProject judges a path that is meant to be relative to the
// directory holding dexaflow.yaml, lexically. An empty value means "not
// declared" and is contained. The cleaned form is returned for the message.
func pathOutsideProject(value string) (string, pathProblem) {
	if value == "" {
		return "", pathContained
	}
	if filepath.IsAbs(value) {
		return value, pathIsAbsolute
	}
	// Clean resolves any ".." before the comparison, so "transform/../analytics"
	// is judged on what it actually points at rather than on how it is spelled.
	clean := filepath.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return clean, pathEscapes
	}
	return clean, pathContained
}
