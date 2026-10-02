package dbt

import (
	"fmt"

	"github.com/dexadata/dexaflow/internal/domain"
)

// Meta carries the DAG metadata a dbt manifest does not provide: identity,
// version, image, ownership, schedule, and the granularity strategy. These come
// from the Dexaflow project config, not from dbt.
type Meta struct {
	// Warn, when set, receives advisories that are not build failures — passed
	// through to Render. nil means say nothing (#1114).
	Warn        func(string)
	DagID       string
	DagVersion  string
	Image       string
	Owner       string
	Description string
	Tags        []string
	Schedule    string
	Granularity Granularity
	// Connections and Variables are the secret names the dexaflow.yaml declares
	// (ADR 0045 / ADR 0055). They must reach the spec: what a task pod is allowed
	// to see is derived from what the DAG declares. Dropping them here does not
	// merely omit a field — under `auth.secret_scoping: enforce` the pod is
	// delivered nothing and the DAG fails inside the task rather than at compile
	// (#997). Under the default permissive scoping the pod still receives the
	// whole tenant vault, so the declarations are simply not honored; the
	// exception there is a secret that lives only in an external backend, which
	// is requested by declared name and so is never fetched at all. The dag.py
	// path emits both; this path built its spec from an explicit field list that
	// omitted them.
	Connections []string
	Variables   []string
	// Connection and Profile, when set, wrap each task's dbt command with the
	// runtime step that writes profiles.yml from the managed connection (ADR 0043).
	Connection string
	Profile    string
	// Schema overrides the dbt target schema in the generated profile.
	Schema string
	// ProjectDir scopes the dbt commands with --project-dir for a subdir project.
	ProjectDir string
	// ProfilesDir, when set, is passed as --profiles-dir. It is how a project
	// that ships its own profiles.yml gets honored: the base image points
	// DBT_PROFILES_DIR at an ephemeral /tmp dir so dbt's WRITES stay off the
	// read-only project (#852), and the side effect is that dbt never looks
	// inside the project at all (#994). Only reads happen through this — target
	// and log paths keep their own env and still land in /tmp. Ignored when a
	// managed connection is set: that path generates a profile into
	// DBT_PROFILES_DIR and must win over a checked-in file.
	ProfilesDir string
	// Local marks a Lite/host build: with no Connection, each task is prefixed with
	// the step that writes a default duckdb profiles.yml (the zero-config local
	// warehouse). Ignored on the Pro/image path. See Options.Local.
	Local bool
}

// Compile renders a dbt manifest.json into tasks and assembles a complete
// dag.json DAGSpec around them. DagID is required; an empty Schedule yields an
// unscheduled DAG (the field is omitted, not set to "").
func Compile(manifestJSON []byte, meta Meta) (domain.DAGSpec, error) {
	if meta.DagID == "" {
		return domain.DAGSpec{}, fmt.Errorf("dbt compile: dag_id is required")
	}
	tasks, err := Render(manifestJSON, Options{
		Granularity: meta.Granularity,
		Warn:        meta.Warn,
		Connection:  meta.Connection,
		Profile:     meta.Profile,
		Schema:      meta.Schema,
		ProjectDir:  meta.ProjectDir,
		ProfilesDir: meta.ProfilesDir,
		Local:       meta.Local,
		// So the DAG's declarations survive the managed connection being
		// stamped on each task (see Options.DagConnections).
		DagConnections: meta.Connections,
	})
	if err != nil {
		return domain.DAGSpec{}, err
	}
	spec := domain.DAGSpec{
		SchemaVersion: "1.0",
		DagID:         meta.DagID,
		DagVersion:    meta.DagVersion,
		Image:         meta.Image,
		Owner:         meta.Owner,
		Description:   meta.Description,
		Tags:          meta.Tags,
		Tasks:         tasks,
		// Copied rather than aliased: the spec outlives the Meta it was built
		// from, and sharing the backing array lets a later append by the caller
		// mutate a spec that has already been written.
		Connections: append([]string(nil), meta.Connections...),
		Variables:   append([]string(nil), meta.Variables...),
	}
	if meta.Schedule != "" {
		schedule := meta.Schedule
		spec.Schedule = &schedule
	}
	return spec, nil
}
