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
	if value == "" {
		return nil
	}
	if filepath.IsAbs(value) {
		return fmt.Errorf("%w: %s %q is absolute; it must be relative to the directory holding dexaflow.yaml, "+
			"because a Pro image build bakes the project at that relative path inside the image",
			ErrInvalidDbtProject, field, value)
	}
	// Clean resolves any ".." before the comparison, so "transform/../analytics"
	// is judged on what it actually points at rather than on how it is spelled.
	if clean := filepath.Clean(value); clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s %q resolves to %q, outside the directory holding dexaflow.yaml; "+
			"the Docker build context cannot reach it, so it would be missing from the image",
			ErrInvalidDbtProject, field, value, clean)
	}
	return nil
}
