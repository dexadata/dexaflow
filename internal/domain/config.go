package domain

import (
	"fmt"
	"strings"

	"github.com/dexadata/dexaflow/internal/connectors"
)

// LeoflowConfig is the developer-facing project configuration parsed from
// dexaflow.yaml. It mirrors docs/api/leoflow-yaml-schema.json and is consumed
// by `dexaflow compile` to build an image and emit a DAGSpec.
type LeoflowConfig struct {
	SchemaVersion string   `json:"schema_version,omitempty" yaml:"schema_version,omitempty"`
	DagID         string   `json:"dag_id" yaml:"dag_id"`
	Description   string   `json:"description,omitempty" yaml:"description,omitempty"`
	Owner         string   `json:"owner,omitempty" yaml:"owner,omitempty"`
	Tags          []string `json:"tags,omitempty" yaml:"tags,omitempty"`
	PythonVersion string   `json:"python_version,omitempty" yaml:"python_version,omitempty"`
	// PythonVersionDefaulted reports that ApplyDefaults supplied PythonVersion
	// because the config declared none. It is not part of the authoring surface
	// and never serialized — it exists so a consumer can tell "the author chose
	// 3.11" from "nobody said anything", which are the same string afterwards.
	// `dexaflow lite` needs the distinction: enforcing a version the CLI invented
	// refuses to boot over a choice the user never made (#1092 follow-up).
	PythonVersionDefaulted bool     `json:"-" yaml:"-"`
	BaseImage              string   `json:"base_image,omitempty" yaml:"base_image,omitempty"`
	Dependencies           []string `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	Connectors             []string `json:"connectors,omitempty" yaml:"connectors,omitempty"`
	// Connections and Variables are the per-DAG declared secret sets (ADR 0045,
	// ADR 0055). Carried verbatim to the parser, which emits them into dag.json.
	// Distinct from Connectors (pip provider packages, ADR 0038) — a different key
	// one letter away. Empty declares nothing.
	Connections    []string        `json:"connections,omitempty" yaml:"connections,omitempty"`
	Variables      []string        `json:"variables,omitempty" yaml:"variables,omitempty"`
	SystemPackages []string        `json:"system_packages,omitempty" yaml:"system_packages,omitempty"`
	DagSource      string          `json:"dag_source,omitempty" yaml:"dag_source,omitempty"`
	IncludePaths   []string        `json:"include_paths,omitempty" yaml:"include_paths,omitempty"`
	ExcludePaths   []string        `json:"exclude_paths,omitempty" yaml:"exclude_paths,omitempty"`
	Build          *BuildConfig    `json:"build,omitempty" yaml:"build,omitempty"`
	Registry       *RegistryConfig `json:"registry,omitempty" yaml:"registry,omitempty"`
	Defaults       *ConfigDefaults `json:"defaults,omitempty" yaml:"defaults,omitempty"`
	// Staging requests the opt-in per-DAG-run shared volume (ADR 0022). It is a
	// Dexaflow deployment concern (not an Airflow DAG attribute), so it lives in
	// dexaflow.yaml and the compiler overlays it onto the produced dag.json.
	Staging *StagingConfig `json:"staging,omitempty" yaml:"staging,omitempty"`
	// Dbt declares a dbt project as the DAG source (ADR 0042). Its presence routes
	// `dexaflow compile` to the dbt renderer instead of the Python parser.
	Dbt *DbtConfig `json:"dbt,omitempty" yaml:"dbt,omitempty"`
	// DbtGroups configures dbt projects embedded as task groups in a dag.py (ADR
	// 0043), keyed by the name passed to `dbt_group(name)`. Schedule does not apply
	// to a group (the DAG owns the schedule).
	DbtGroups map[string]*DbtConfig `json:"dbt_groups,omitempty" yaml:"dbt_groups,omitempty"`
	// Tasks holds per-task overrides bound by task_id (ADR 0023). Each entry's
	// key must match a task_id in the compiled DAG; the compiler errors on an
	// unknown id rather than silently dropping it.
	Tasks map[string]*TaskConfig `json:"tasks,omitempty" yaml:"tasks,omitempty"`
	// Alerts declares native on-failure alerting (#424): the scheduler fires the
	// listed rules when a DagRun reaches the terminal failed state, in Go, with no
	// task pod and no Python in the hot path. A Dexaflow deployment concern (not an
	// Airflow DAG attribute), so it lives in dexaflow.yaml and the compiler overlays
	// it onto the produced dag.json.
	Alerts *AlertsConfig `json:"alerts,omitempty" yaml:"alerts,omitempty"`
	// MinIdleWorkers is the number of idle warm workers the DAG asks to keep
	// for its current version (ADR 0058, ADR 0082): with warm pools on, an
	// unpaused DAG that declares it is prewarmed from the moment a version
	// becomes current. The compiler overlays it onto dag.json; the server
	// clamps it to execution.max_pool_size, and with warm pools off it does
	// nothing. Zero declares none.
	MinIdleWorkers int `json:"min_idle_workers,omitempty" yaml:"min_idle_workers,omitempty"`
}

// AlertsConfig groups alert rules by the lifecycle event that fires them. Only
// on_failure is wired today (#424); on_success/on_retry are reserved for a later
// increment so the surface can grow without a breaking change.
type AlertsConfig struct {
	// OnFailure lists the rules dispatched when a DagRun reaches failed.
	OnFailure []AlertRule `json:"on_failure,omitempty" yaml:"on_failure,omitempty"`
}

// AlertRule is one channel to notify on an alert event. The endpoint and its
// secret always come from a managed connection (Conn), never a literal URL or
// token in dexaflow.yaml — that keeps credentials out of the compiled dag.json and
// mirrors the env-ref secret discipline.
type AlertRule struct {
	// Type is the channel: "slack" (Slack incoming webhook) or "webhook" (a generic
	// HTTP POST, e.g. PagerDuty/Opsgenie/Teams). Validated by the schema enum.
	Type string `json:"type" yaml:"type"`
	// Conn is the managed Dexaflow connection id holding the endpoint (and secret).
	Conn string `json:"conn" yaml:"conn"`
	// Message is the optional notification body; it is templated at fire time with
	// run context ({{dag}}, {{run_id}}, {{task}}, …). Empty uses a default summary.
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// DbtConfig declares a dbt project as the DAG source (ADR 0042). The compiler
// reads the project's manifest.json and renders one task per dbt node (or per
// group), so a dbt project becomes a Dexaflow DAG with no Cosmos or Airflow.
type DbtConfig struct {
	// Project is the directory containing dbt_project.yml.
	Project string `json:"project,omitempty" yaml:"project,omitempty"`
	// Granularity is the task partition strategy: node, level or folder (ADR 0042
	// §5). Empty means node. Tag and selector grouping are not implemented — the
	// schema enum is the three above; see #398.
	Granularity string `json:"granularity,omitempty" yaml:"granularity,omitempty"`
	// Manifest optionally points to a pre-built manifest.json (the Pro/CI baked
	// path); empty means run `dbt parse` to generate it at compile time.
	Manifest string `json:"manifest,omitempty" yaml:"manifest,omitempty"`
	// Schedule is the DAG's cron expression or preset (e.g. "@daily",
	// "0 6 * * *"). dbt carries no schedule, so it is declared here; empty means
	// an unscheduled DAG (run on demand).
	Schedule string `json:"schedule,omitempty" yaml:"schedule,omitempty"`
	// Connection is a managed Dexaflow connection id (ADR 0043 #2). When set, the
	// dbt task generates its profiles.yml from the connection delivered to the pod
	// instead of a profiles.yml baked into the image — use one or the other.
	Connection string `json:"connection,omitempty" yaml:"connection,omitempty"`
	// Schema overrides the dbt target schema in the generated profile (where models
	// materialize); empty uses the connection's or dbt's default.
	Schema string `json:"schema,omitempty" yaml:"schema,omitempty"`
}

// TaskConfig holds the dexaflow.yaml per-task overrides bound by task_id (ADR
// 0023). Every field is optional; a set field overrides the value compiled from
// the DAG (most specific wins: task override > DAG default_args). These are
// Dexaflow deployment concerns, not Airflow operator attributes.
type TaskConfig struct {
	Retries                 *int              `json:"retries,omitempty" yaml:"retries,omitempty"`
	RetryDelaySeconds       *int              `json:"retry_delay_seconds,omitempty" yaml:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds *int              `json:"execution_timeout_seconds,omitempty" yaml:"execution_timeout_seconds,omitempty"`
	Env                     map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// Connections and Variables narrow the DAG-level declared secret set to this
	// task (ADR 0045 §Settled #1, ADR 0055). Empty means the task inherits the
	// DAG-level declaration.
	Connections []string   `json:"connections,omitempty" yaml:"connections,omitempty"`
	Variables   []string   `json:"variables,omitempty" yaml:"variables,omitempty"`
	Resources   *Resources `json:"resources,omitempty" yaml:"resources,omitempty"`
	Execution   *Execution `json:"execution,omitempty" yaml:"execution,omitempty"`
	// Size is the task's pool_slots (ADR 0066): how many slots of its pool it
	// takes while queued or running. It overrides pool_slots from dag.py.
	Size *int `json:"size,omitempty" yaml:"size,omitempty"`
}

// BuildConfig controls how the container image is built from the project.
type BuildConfig struct {
	Dockerfile string            `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty"`
	Context    string            `json:"context,omitempty" yaml:"context,omitempty"`
	Platforms  []string          `json:"platforms,omitempty" yaml:"platforms,omitempty"`
	Labels     map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	// RemoteBuilder is the remote builder for builds no daemonless path
	// covers (ADR 0083); nil leaves those to the target's or workspace's
	// remote_builder, then the local daemon.
	RemoteBuilder *RemoteBuilderConfig `json:"remote_builder,omitempty" yaml:"remote_builder,omitempty"`
	// Args are the build arguments (`--build-arg`) the project's own
	// Dockerfile reads with ARG. Plain values: they stay in the image
	// history, so a secret goes in Secrets.
	Args map[string]string `json:"args,omitempty" yaml:"args,omitempty"`
	// Secrets are the build secrets a RUN line mounts with
	// --mount=type=secret,id=<id>, read from the environment of the build.
	Secrets []BuildSecret `json:"secrets,omitempty" yaml:"secrets,omitempty"`
	// PackageIndex is the Python package index pip and uv install from in
	// place of PyPI; nil is PyPI.
	PackageIndex *PackageIndex `json:"package_index,omitempty" yaml:"package_index,omitempty"`
}

// RegistryConfig describes where the built image is pushed and how it is tagged.
type RegistryConfig struct {
	URL string `json:"url,omitempty" yaml:"url,omitempty"`
	// AuthMethod is where the CLI reads the registry credential: auto (the
	// default), docker_config or cloud; env and iam still load, deprecated
	// (build.ParseAuthMethod, ADR 0080).
	AuthMethod  string `json:"auth_method,omitempty" yaml:"auth_method,omitempty"`
	ImageName   string `json:"image_name,omitempty" yaml:"image_name,omitempty"`
	TagStrategy string `json:"tag_strategy,omitempty" yaml:"tag_strategy,omitempty"`
}

// ConfigDefaults holds task defaults applied to every task generated from the
// project at compile time.
type ConfigDefaults struct {
	Retries                 int `json:"retries,omitempty" yaml:"retries,omitempty"`
	RetryDelaySeconds       int `json:"retry_delay_seconds,omitempty" yaml:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds int `json:"execution_timeout_seconds,omitempty" yaml:"execution_timeout_seconds,omitempty"`
	// Size is the default pool_slots of every task that sets none, in dag.py or
	// in tasks.<id>.size (ADR 0066).
	Size      *int              `json:"size,omitempty" yaml:"size,omitempty"`
	Resources *DefaultResources `json:"resources,omitempty" yaml:"resources,omitempty"`
	// NodeSelector is the DAG-wide pod placement fallback applied to every task
	// that declares no execution.node_selector of its own. Like Resources it is a
	// default, so the most-specific per-task value always wins. Consumed at
	// compile time by the overlay, which bakes it onto each task in dag.json.
	NodeSelector map[string]string `json:"node_selector,omitempty" yaml:"node_selector,omitempty"`
}

// DefaultResources expresses default CPU and memory for generated tasks.
type DefaultResources struct {
	CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
}

// AsResources expands the simplified default cpu/memory into a full Resources
// with requests == limits (the QoS story of #725). This mirrors how the
// per-cluster platform default is built at dispatch. Returns nil when the
// receiver is nil or declares no quantity, so callers can treat a missing
// default as "leave the task untouched".
//
// Guaranteed QoS is reached only when BOTH cpu and memory are set (and thus
// equal across requests and limits). A partial default — only cpu, or only
// memory — leaves the other dimension unset, so Kubernetes classifies the pod
// as Burstable, not Guaranteed.
func (d *DefaultResources) AsResources() *Resources {
	if d == nil || (d.CPU == "" && d.Memory == "") {
		return nil
	}
	return &Resources{
		Requests: &ResourceQuantity{CPU: d.CPU, Memory: d.Memory},
		Limits:   &ResourceQuantity{CPU: d.CPU, Memory: d.Memory},
	}
}

// ApplyDefaults fills zero-valued fields with the defaults declared in the
// canonical JSON Schema (internal/domain/schemas/leoflow-yaml-schema.json).
// Explicit user-set values are preserved; nested structs (Build, Registry)
// are instantiated when nil so their own defaults can be applied. The method
// is idempotent: a second call after the first is a no-op.
//
// Centralizing defaults here (instead of scattered `if x == ""` fallbacks at
// each consumer) is what lets the multi-DAG workspace synthesize a working
// config when a subdir ships no dexaflow.yaml, while keeping the resolved
// values debuggable from one place.
func (c *LeoflowConfig) ApplyDefaults() {
	if c.SchemaVersion == "" {
		c.SchemaVersion = "1.0"
	}
	if c.PythonVersion == "" {
		c.PythonVersion = "3.11"
		c.PythonVersionDefaulted = true
	}
	if c.DagSource == "" {
		c.DagSource = "dag.py"
	}
	if c.IncludePaths == nil {
		c.IncludePaths = []string{"."}
	}
	if c.ExcludePaths == nil {
		c.ExcludePaths = []string{".git", "__pycache__", "*.pyc", ".venv", "venv"}
	}
	if c.Build == nil {
		c.Build = &BuildConfig{}
	}
	if c.Build.Context == "" {
		c.Build.Context = "."
	}
	if c.Build.Platforms == nil {
		c.Build.Platforms = []string{"linux/amd64"}
	}
	if c.Registry == nil {
		c.Registry = &RegistryConfig{}
	}
	if c.Registry.AuthMethod == "" {
		c.Registry.AuthMethod = "auto"
	}
	if c.Registry.TagStrategy == "" {
		c.Registry.TagStrategy = "version"
	}
}

// EffectiveDependencies resolves the full pip install list the image/venv needs:
// the `connectors:` short names expanded to their apache-airflow-providers-*
// packages (ADR 0038's sugar), followed by the explicit `dependencies:` verbatim.
// Providers come first so a transitive driver pinned in dependencies resolves
// against the provider declared via the sugar.
//
// An unknown connector name is a compile error, not a silent drop: a typo that
// slipped through would otherwise surface as a ModuleNotFoundError inside the
// task pod, far from its cause. The message names the offender, lists the known
// types, and points at the dependencies: escape hatch.
func (c *LeoflowConfig) EffectiveDependencies() ([]string, error) {
	packages, unknown := connectors.Resolve(c.Connectors)
	if len(unknown) > 0 {
		return nil, fmt.Errorf(
			"unknown connector(s) %s in connectors:; known: %s; "+
				"or add the pip package to dependencies: directly",
			strings.Join(unknown, ", "),
			strings.Join(connectors.Types(), ", "),
		)
	}
	if len(packages) == 0 && len(c.Dependencies) == 0 {
		return nil, nil
	}
	effective := make([]string, 0, len(packages)+len(c.Dependencies))
	effective = append(effective, packages...)
	effective = append(effective, c.Dependencies...)
	return effective, nil
}

// Validate checks the LeoflowConfig against the canonical dexaflow.yaml schema
// and returns a joined error describing every violation, or nil when valid.
func (c *LeoflowConfig) Validate() error {
	s, err := schemas()
	if err != nil {
		return err
	}
	if err := c.validateRetiredPython(); err != nil {
		return err
	}
	if err := validateAgainst(s.leoflow, c); err != nil {
		return err
	}
	if err := c.validateAlertTemplates(); err != nil {
		return err
	}
	if err := c.validateDbtProject(); err != nil {
		return err
	}
	if err := c.validateBuildDockerfile(); err != nil {
		return err
	}
	if err := c.validateMinIdleWorkers(); err != nil {
		return err
	}
	if c.Build != nil {
		if err := c.Build.RemoteBuilder.Validate("build.remote_builder"); err != nil {
			return err
		}
		if err := c.Build.validateInputs(); err != nil {
			return err
		}
	}
	return c.ValidateDockerfileValues()
}

// validateRetiredPython refuses a python_version that is no longer published
// with a message that names the replacement. It runs before the schema, whose
// enum would refuse the same value as if it were a typo (#1447).
func (c *LeoflowConfig) validateRetiredPython() error {
	r, ok := RetiredPythonVersion(c.PythonVersion)
	if !ok {
		return nil
	}
	return fmt.Errorf("python_version %s is no longer published: no release cut after %s ships a py%s task base. "+
		"Set python_version: %q in dexaflow.yaml and rebuild; existing images keep running",
		r.Version, r.RemoveAfter, r.Version, r.Replacement)
}

// validateMinIdleWorkers refuses min_idle_workers on a DAG with staging
// enabled: a staging version never gets a warm pool (ADR 0058 D5), so the
// declaration would be accepted and do nothing.
func (c *LeoflowConfig) validateMinIdleWorkers() error {
	if c.MinIdleWorkers > 0 && c.Staging != nil && c.Staging.Enabled {
		return fmt.Errorf("min_idle_workers %d has no effect with staging.enabled: "+
			"a staging DAG's tasks always run on dedicated pods (ADR 0058); remove one of the two", c.MinIdleWorkers)
	}
	return nil
}
