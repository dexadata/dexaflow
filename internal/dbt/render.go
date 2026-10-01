// Package dbt renders a dbt project's manifest.json into Leoflow tasks (ADR 0042).
//
// dbt already compiles a project into target/manifest.json — the canonical DAG of
// nodes (seeds, models, snapshots, tests) with their dependencies. This package
// reads that file in Go and emits flat Leoflow tasks, so a dbt project becomes a
// Leoflow DAG with no Cosmos at runtime and no Airflow in the parser.
package dbt

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
)

// Granularity controls how dbt nodes are partitioned into Leoflow tasks.
type Granularity string

// Granularity strategies. node is one task per dbt node; the rest contract a
// group of nodes into one task (a quotient of the dbt DAG), which must stay
// acyclic — the semantic strategy (folder) is validated, level is acyclic by
// construction. Tag/selector grouping is tracked separately (issue #398) because
// its overlap semantics (a node with several tags) needs a deliberate rule.
const (
	GranularityNode   Granularity = "node"
	GranularityLevel  Granularity = "level"
	GranularityFolder Granularity = "folder"
)

// Options configures a Render call.
type Options struct {
	// Granularity selects the partition strategy; the empty value means node.
	Granularity Granularity
	// Warn, when set, receives advisories that are not build failures — today
	// only a folder group-key collision (#1114). This package is otherwise pure,
	// so the caller decides where a warning goes; nil means say nothing.
	Warn func(string)
	// Connection, when set, is a managed Leoflow connection id; each task's dbt
	// command is prefixed with the runtime step that writes profiles.yml from it
	// (ADR 0043 #2), so no credential is baked into the image.
	Connection string
	// Profile is the dbt project's profile name (dbt_project.yml `profile:`), used
	// as the generated profiles.yml key. Meaningful only when Connection is set.
	Profile string
	// Schema, when set, overrides the dbt target schema in the generated profile
	// (otherwise the connection's or the "public" default is used).
	Schema string
	// ProjectDir scopes each command with --project-dir so dbt finds dbt_project.yml
	// when the project is a subdirectory of the DAG (#401). "." or empty adds nothing.
	ProjectDir string
	// ProfilesDir, when set, is passed as --profiles-dir. It is how a project that
	// ships its own profiles.yml gets honored: the base image points
	// DBT_PROFILES_DIR at an ephemeral /tmp dir so dbt's WRITES stay off the
	// read-only project (#852), and the side effect is that dbt never looks inside
	// the project at all (#994). Only reads go through this — DBT_TARGET_PATH and
	// DBT_LOG_PATH still carry the writes to /tmp. Ignored when Connection is set:
	// that path generates a profile into DBT_PROFILES_DIR and must win over a
	// file checked into the project.
	ProfilesDir string
	// Local marks a Lite/host build: with no Connection, each task is prefixed with a
	// step that writes a default duckdb profiles.yml — a zero-config local warehouse,
	// no server and no connection needed (L4). Ignored on the Pro/image path.
	Local bool
	// DagConnections are the connection ids declared at the top level of
	// leoflow.yaml. They are stamped onto each task ALONGSIDE Connection, not
	// instead of it: declaredConnections (internal/storage) returns the task's
	// list whenever it is non-empty, so stamping only the managed connection
	// shadows the DAG's own declarations and they never reach the pod — a
	// pre-hook's connection silently missing under enforce scoping or an
	// external secrets backend.
	DagConnections []string
}

// dbtVerb maps each executable dbt resource type to the dbt subcommand that runs
// a single node of that type via --select. Non-executable resources (sources,
// macros, exposures, …) are absent and never become tasks.
var dbtVerb = map[string]string{
	"seed":     "seed",
	"model":    "run",
	"snapshot": "snapshot",
	"test":     "test",
}

// manifest is the subset of dbt's manifest.json this package reads.
type manifest struct {
	Nodes map[string]manifestNode `json:"nodes"`
}

type manifestNode struct {
	ResourceType string   `json:"resource_type"`
	Name         string   `json:"name"`
	FQN          []string `json:"fqn"`
	Config       struct {
		Materialized string `json:"materialized"`
	} `json:"config"`
	DependsOn struct {
		Nodes []string `json:"nodes"`
	} `json:"depends_on"`
}

// isEphemeral reports whether a node is an ephemeral model — inlined by dbt as a
// CTE, with no table, so it is not a task but a pass-through for dependencies.
func (m manifest) isEphemeral(id string) bool {
	n := m.Nodes[id]
	return n.ResourceType == "model" && n.Config.Materialized == "ephemeral"
}

// taskParents resolves a node's executable parents, walking through ephemeral
// parents (which are inlined) to inherit their executable ancestors. Returns
// sorted parent ids drawn from taskSet.
func (m manifest) taskParents(id string, taskSet map[string]execNode) []string {
	result := map[string]bool{}
	visited := map[string]bool{}
	var walk func(nodeID string)
	walk = func(nodeID string) {
		for _, p := range m.Nodes[nodeID].DependsOn.Nodes {
			if _, isTask := taskSet[p]; isTask {
				result[p] = true
				continue
			}
			if m.isEphemeral(p) && !visited[p] {
				visited[p] = true
				walk(p)
			}
		}
	}
	walk(id)
	return sortedSetKeys(result)
}

// execNode is an executable dbt node with its executable parents resolved.
type execNode struct {
	name    string
	rtype   string
	fqn     []string
	parents []string // ids of executable parent nodes
}

// Render parses a dbt manifest.json and returns Leoflow bash tasks. At node
// granularity it emits one task per executable node (`dbt <verb> --select`); at a
// grouped granularity it contracts nodes into per-group tasks
// (`dbt build --select`) and rejects a grouping that makes the task graph cyclic.
// Tasks are sorted by TaskID for deterministic output.
func Render(manifestJSON []byte, opts Options) ([]domain.TaskSpec, error) {
	var m manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return nil, fmt.Errorf("parsing dbt manifest: %w", err)
	}

	nodes := make(map[string]execNode, len(m.Nodes))
	for id, n := range m.Nodes {
		if _, ok := dbtVerb[n.ResourceType]; ok && !m.isEphemeral(id) {
			nodes[id] = execNode{name: n.Name, rtype: n.ResourceType, fqn: n.FQN}
		}
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("dbt manifest has no executable nodes (need at least one seed/model/snapshot/test)")
	}
	// Two nodes that share a name (e.g. the same model across installed packages)
	// would collide into one task_id. Reject loudly rather than silently drop one.
	seen := make(map[string]string, len(nodes))
	for _, id := range sortedSetKeys(nodes) {
		name := nodes[id].name
		if prev, dup := seen[name]; dup {
			return nil, fmt.Errorf("dbt nodes %q and %q share the name %q; task_ids must be unique — rename one (cross-package collisions need unique_id namespacing, issue #398)", prev, id, name)
		}
		seen[name] = id
	}
	// Resolve each task's executable parents, walking through ephemeral models
	// (inlined by dbt) so a downstream task keeps the correct upstream ordering.
	for id := range nodes {
		en := nodes[id]
		en.parents = m.taskParents(id, nodes)
		nodes[id] = en
	}

	var tasks []domain.TaskSpec
	if opts.Granularity == "" || opts.Granularity == GranularityNode {
		tasks = renderNodes(nodes)
	} else {
		grouped, gerr := renderGrouped(nodes, opts.Granularity, opts.Warn)
		if gerr != nil {
			return nil, gerr
		}
		tasks = grouped
	}
	decorateCommands(tasks, opts)
	return tasks, nil
}

// decorateCommands scopes each task's dbt command to a subdir project (--project-dir)
// and, for a managed connection, prefixes the runtime profile-generation step.
func decorateCommands(tasks []domain.TaskSpec, opts Options) {
	if opts.ProjectDir != "" && opts.ProjectDir != "." {
		for i := range tasks {
			tasks[i].Entrypoint += " --project-dir " + opts.ProjectDir
		}
	}
	var prefix string
	switch {
	case opts.Connection != "":
		profileCmd := fmt.Sprintf("python -m leoflow_runtime --dbt-profile %s %s", opts.Connection, opts.Profile)
		if opts.Schema != "" {
			profileCmd += " " + opts.Schema
		}
		prefix = profileCmd + " && "
	case opts.Local && opts.Profile != "":
		// Zero-config local warehouse: a default duckdb, no connection needed (L4).
		db := "leoflow_local.duckdb"
		if opts.ProjectDir != "" && opts.ProjectDir != "." {
			db = filepath.Join(opts.ProjectDir, db)
		}
		prefix = fmt.Sprintf("python -m leoflow_runtime --dbt-default-duckdb %s %s && ", opts.Profile, db)
	default:
		// No managed connection: the profile comes from the project's own
		// profiles.yml, which dbt cannot find on its own because the base image
		// points DBT_PROFILES_DIR at /tmp (#852, #994).
		if opts.ProfilesDir != "" {
			for i := range tasks {
				tasks[i].Entrypoint += " --profiles-dir " + opts.ProfilesDir
			}
		}
		return
	}
	for i := range tasks {
		tasks[i].Entrypoint = prefix + tasks[i].Entrypoint
		// Declare the managed connection the profile step reads, so the agent
		// injects AIRFLOW_CONN_<conn> under ANY scoping mode and resolves it from an
		// external backend — declaration is the scope authority (ADR 0055 / 0060).
		// Without this, a dbt DAG with a connection breaks under enforce scoping and
		// with the external secrets backend: the profile step reads an env the agent
		// never delivered (#10).
		if opts.Connection != "" {
			tasks[i].Connections = mergeConnections(tasks[i].Connections, opts.Connection, opts.DagConnections)
		}
	}
}

// renderNodes emits one task per node, scoped to that node's own dbt verb
// (`dbt run`/`seed`/`snapshot`/`test`). A model and its tests are therefore
// SEPARATE tasks — maximum per-model parallelism, retry, and observability. This
// differs from the grouped path (renderGrouped), which uses `dbt build` to run a
// whole group's mixed resource types in one invocation.
func renderNodes(nodes map[string]execNode) []domain.TaskSpec {
	tasks := make([]domain.TaskSpec, 0, len(nodes))
	for _, n := range nodes {
		var deps []string
		if len(n.parents) > 0 {
			deps = make([]string, 0, len(n.parents))
			for _, p := range n.parents {
				deps = append(deps, nodes[p].name)
			}
			sort.Strings(deps)
		}
		tasks = append(tasks, domain.TaskSpec{
			TaskID:     n.name,
			Type:       domain.TaskTypeBash,
			Entrypoint: fmt.Sprintf("dbt %s --select %s", dbtVerb[n.rtype], n.name),
			DependsOn:  deps,
		})
	}
	sortTasks(tasks)
	return tasks
}

// renderGrouped partitions nodes by the strategy and contracts each group into
// one `dbt build --select <members>` task — build runs the group's seeds, models,
// snapshots, and tests in dbt's own internal order, so a group may mix resource
// types. It fails if the resulting quotient graph is cyclic (see findCycle).
func renderGrouped(nodes map[string]execNode, gran Granularity, warn func(string)) ([]domain.TaskSpec, error) {
	levels := topoLevels(nodes)
	groupOf := make(map[string]string, len(nodes))
	for id, n := range nodes {
		groupOf[id] = groupKey(n, gran, levels[id])
	}

	members := map[string][]string{}
	children := map[string]map[string]bool{} // parent group -> child groups
	parents := map[string]map[string]bool{}  // child group -> parent groups
	// origins tracks WHY a node landed in a group: the folder segment it came
	// from, or "" for a node with no folder at all (which falls back to the
	// resource-type plural). Two different origins reaching the same key is the
	// collision — a folder literally named `models` plus a model at the root of
	// models/ both key on `models`, and their two sets merge into one task. The
	// same holds for any resource type: a folder named `seeds` plus a seed at the
	// project root collide on `seeds`.
	origins := map[string]map[string]bool{}
	for id, n := range nodes {
		g := groupOf[id]
		members[g] = append(members[g], n.name)
		if gran == GranularityFolder {
			origin := ""
			if len(n.fqn) > 2 {
				origin = n.fqn[1]
			}
			if origins[g] == nil {
				origins[g] = map[string]bool{}
			}
			origins[g][origin] = true
		}
		for _, p := range n.parents {
			pg := groupOf[p]
			if pg == g {
				continue
			}
			addEdge(children, pg, g)
			addEdge(parents, g, pg)
		}
	}

	if cyc := findCycle(members, children); cyc != nil {
		return nil, fmt.Errorf(
			"dbt grouping by %s introduces a cross-group cycle: %s; use granularity=node or reorganize the project",
			gran, strings.Join(append(cyc, cyc[0]), " -> "))
	}

	// Refuse before warning: a group that is BOTH unusably named and collided
	// should fail, not warn and then fail. The advisory would otherwise be the
	// last thing on stderr before an unrelated-looking refusal.
	if verr := checkDerivedTaskIDs(members, gran); verr != nil {
		return nil, verr
	}
	warnFolderCollisions(warn, gran, members, origins)

	tasks := make([]domain.TaskSpec, 0, len(members))
	for g, mem := range members {
		sort.Strings(mem)
		tasks = append(tasks, domain.TaskSpec{
			TaskID:     g,
			Type:       domain.TaskTypeBash,
			Entrypoint: "dbt build --select " + strings.Join(mem, " "),
			DependsOn:  sortedSet(parents[g]),
		})
	}
	sortTasks(tasks)
	return tasks, nil
}

// groupKey returns the group a node belongs to under the given strategy.
func groupKey(n execNode, gran Granularity, level int) string {
	switch gran {
	case GranularityLevel:
		return fmt.Sprintf("level_%d", level)
	case GranularityFolder:
		if len(n.fqn) > 2 {
			return n.fqn[1] // <package>/<folder>/.../<name>
		}
		return n.rtype + "s" // seeds/snapshots carry no folder segment
	default:
		return n.name
	}
}

// topoLevels assigns level(n) = 0 for a node with no executable parents, else
// 1 + max(level(parents)). Grouping by level is acyclic by construction.
func topoLevels(nodes map[string]execNode) map[string]int {
	lvl := make(map[string]int, len(nodes))
	var visit func(id string) int
	visit = func(id string) int {
		if v, ok := lvl[id]; ok {
			return v
		}
		maxLvl := -1
		for _, p := range nodes[id].parents {
			if l := visit(p); l > maxLvl {
				maxLvl = l
			}
		}
		lvl[id] = maxLvl + 1
		return lvl[id]
	}
	for id := range nodes {
		visit(id)
	}
	return lvl
}

// findCycle runs a 3-color DFS over the quotient graph and returns a cycle path
// (group names) or nil. Deterministic via sorted iteration.
func findCycle(members map[string][]string, children map[string]map[string]bool) []string {
	const white, gray, black = 0, 1, 2
	color := make(map[string]int, len(members))
	var stack []string
	var dfs func(g string) []string
	dfs = func(g string) []string {
		color[g] = gray
		stack = append(stack, g)
		for _, to := range sortedSet(children[g]) {
			switch color[to] {
			case gray:
				for i, s := range stack {
					if s == to {
						return append([]string(nil), stack[i:]...)
					}
				}
			case white:
				if c := dfs(to); c != nil {
					return c
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[g] = black
		return nil
	}
	for _, g := range sortedSetKeys(members) {
		if color[g] == white {
			if c := dfs(g); c != nil {
				return c
			}
		}
	}
	return nil
}

func addEdge(m map[string]map[string]bool, from, to string) {
	if m[from] == nil {
		m[from] = map[string]bool{}
	}
	m[from][to] = true
}

func sortTasks(tasks []domain.TaskSpec) {
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
}

func sortedSet(s map[string]bool) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedSetKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// taskIDPattern mirrors the dag schema's task_id rule. It is duplicated here on
// purpose: the schema is the authority and still rejects a bad id, but it does
// so at the END of the compile, as a jsonschema failure naming dag.json. A dbt
// folder becomes a task id verbatim, so this package knows both the offending
// folder and the rule, and is the only place that can say which folder.
var taskIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_-]{0,199}$`)

// checkDerivedTaskIDs refuses a group key that cannot be a task id, naming the
// folder and the knob that produced it (#1114).
func checkDerivedTaskIDs(members map[string][]string, gran Granularity) error {
	keys := make([]string, 0, len(members))
	for g := range members {
		keys = append(keys, g)
	}
	sort.Strings(keys)
	for _, g := range keys {
		if taskIDPattern.MatchString(g) {
			continue
		}
		mem := append([]string(nil), members[g]...)
		sort.Strings(mem)
		return fmt.Errorf("dbt granularity=%s derives the task id %q from your project, and it is not a usable task id (letters, digits, underscore, then letters/digits/underscore/hyphen, up to 200 chars). It groups %s — rename it in the dbt project, or use granularity=node so each model keeps its own name",
			gran, g, strings.Join(mem, ", "))
	}
	return nil
}

// warnFolderCollisions announces a folder group whose members arrived from more
// than one origin (#1114).
//
// The merge is NOT refused. dbt still orders the nodes inside the combined
// task, so the data is not wrong — what is lost is Leoflow-level parallelism and
// per-node failure isolation. Refusing would break a project that runs today;
// staying silent leaves an author looking at one task named after a folder with
// no indication that a root-level model was folded into it. Saying so loudly is
// the only option that is neither.
func warnFolderCollisions(warn func(string), gran Granularity, members map[string][]string, origins map[string]map[string]bool) {
	if warn == nil || gran != GranularityFolder {
		return
	}
	keys := make([]string, 0, len(origins))
	for g := range origins {
		if len(origins[g]) > 1 {
			keys = append(keys, g)
		}
	}
	sort.Strings(keys)
	for _, g := range keys {
		mem := append([]string(nil), members[g]...)
		sort.Strings(mem)
		// "nodes", not "models": a seed or snapshot at the project root falls
		// back to the same key (groupKey uses rtype+"s"), so a model in a folder
		// named `seeds` plus a root seed lands here — and calling the second set
		// models would send the author looking in the wrong place.
		warn(fmt.Sprintf("dbt granularity=folder: group %q holds nodes from a folder named %q AND nodes with no folder, so they run as ONE task (%s) — dbt still orders them, but they lose per-node isolation and parallelism. Rename the folder, or use granularity=node.",
			g, g, strings.Join(mem, ", ")))
	}
}

// mergeConnections stamps the managed dbt connection onto a task together with
// the connections the DAG declared.
//
// Both, never just the managed one: once a task carries ANY connection list,
// that list is what reaches the pod (declaredConnections, internal/storage,
// consults the DAG-level list only when the task's is empty). Stamping the
// managed connection alone therefore silently drops every connection the DAG
// declared — a pre-hook's warehouse missing under enforce scoping or an external
// secrets backend, failing inside the task.
func mergeConnections(existing []string, managed string, declared []string) []string {
	// A fresh slice: appending onto the caller's would share its backing array.
	out := make([]string, 0, len(existing)+1+len(declared))
	out = append(out, existing...)
	out = append(out, managed)
	for _, c := range declared {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}
