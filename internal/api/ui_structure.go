package api

import (
	"container/heap"
	"context"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// DagSpecReader reads the parsed spec of a DAG's current version, the source of
// task topology for the grid and graph views.
type DagSpecReader interface {
	GetCurrentSpec(ctx context.Context, tenant, dagID string) (domain.DAGSpec, error)
}

// gridNodeDTO is the Airflow 3.2.1 GridNodeResponse — one task in the grid's
// left column. children is null in the MVP (no task groups); is_mapped is false
// (no dynamic task mapping).
type gridNodeDTO struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	IsMapped bool           `json:"is_mapped"`
	Children *[]gridNodeDTO `json:"children"`
}

// structureNodeDTO is the Airflow 3.2.1 NodeResponse for the graph view. The
// graph's React Flow renderer reads the full node shape, so the optional fields
// are emitted as null (matching real Airflow) rather than omitted — omitting
// them leaves the graph canvas blank.
type structureNodeDTO struct {
	ID                 string              `json:"id"`
	Label              string              `json:"label"`
	Type               string              `json:"type"`
	Children           *[]structureNodeDTO `json:"children"`
	IsMapped           *bool               `json:"is_mapped"`
	Tooltip            *string             `json:"tooltip"`
	SetupTeardownType  *string             `json:"setup_teardown_type"`
	Operator           *string             `json:"operator"`
	AssetConditionType *string             `json:"asset_condition_type"`
}

// structureEdgeDTO is the Airflow 3.2.1 EdgeResponse: a dependency from upstream
// (source_id) to downstream (target_id), with the optional graph-render fields
// present as null.
type structureEdgeDTO struct {
	SourceID        string  `json:"source_id"`
	TargetID        string  `json:"target_id"`
	IsSetupTeardown *bool   `json:"is_setup_teardown"`
	Label           *string `json:"label"`
	IsSourceAsset   *bool   `json:"is_source_asset"`
}

// structureDataDTO is the Airflow 3.2.1 StructureDataResponse.
type structureDataDTO struct {
	Nodes []structureNodeDTO `json:"nodes"`
	Edges []structureEdgeDTO `json:"edges"`
}

// topoSortTasks orders tasks so every task follows its declared upstreams
// (Kahn's algorithm), breaking ties by task_id for a stable layout. Unknown
// dependencies are ignored; a cyclic remainder (which a valid DAG never has) is
// appended in id order so no task is dropped.
func topoSortTasks(tasks []domain.TaskSpec) []domain.TaskSpec {
	byID := make(map[string]domain.TaskSpec, len(tasks))
	indeg := make(map[string]int, len(tasks))
	children := make(map[string][]string, len(tasks))
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		byID[t.TaskID] = t
		indeg[t.TaskID] = 0
		ids = append(ids, t.TaskID)
	}
	for _, t := range tasks {
		for _, dep := range t.DependsOn {
			if _, ok := byID[dep]; !ok {
				continue
			}
			indeg[t.TaskID]++
			children[dep] = append(children[dep], t.TaskID)
		}
	}
	// The ready set is a min-heap, so taking the smallest id is O(log n) instead
	// of re-sorting the whole set after every pop (68 ms at 5,000 tasks).
	ready := make(readyHeap, 0, len(tasks))
	for _, id := range ids {
		if indeg[id] == 0 {
			ready = append(ready, id)
		}
	}
	heap.Init(&ready)
	out := make([]domain.TaskSpec, 0, len(tasks))
	for ready.Len() > 0 {
		id := ready.popMin()
		out = append(out, byID[id])
		for _, ch := range children[id] {
			indeg[ch]--
			if indeg[ch] == 0 {
				ready.push(ch)
			}
		}
	}
	if len(out) < len(tasks) {
		seen := make(map[string]bool, len(out))
		for _, t := range out {
			seen[t.TaskID] = true
		}
		for _, id := range ids {
			if !seen[id] {
				out = append(out, byID[id])
			}
		}
	}
	return out
}

// readyHeap is a min-heap of task ids (container/heap), the ready set of
// topoSortTasks. Equal ids pop in the same order a sorted slice would give them.
type readyHeap []string

func (h readyHeap) Len() int           { return len(h) }
func (h readyHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h readyHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

// Push appends x, which is always a string; heap.Push restores the order.
func (h *readyHeap) Push(x any) {
	if id, ok := x.(string); ok {
		*h = append(*h, id)
	}
}

// Pop removes the last element; heap.Pop has already moved the minimum there.
func (h *readyHeap) Pop() any {
	old := *h
	id := old[len(old)-1]
	*h = old[:len(old)-1]
	return id
}

// push adds id. It is heap.Push without boxing the string in an interface.
func (h *readyHeap) push(id string) {
	*h = append(*h, id)
	heap.Fix(h, len(*h)-1)
}

// popMin removes and returns the smallest id, as heap.Pop would.
func (h *readyHeap) popMin() string {
	old := *h
	n := len(old) - 1
	old.Swap(0, n)
	id := old[n]
	*h = old[:n]
	if n > 0 {
		heap.Fix(h, 0)
	}
	return id
}

// gridStructureHandler implements GET /ui/grid/structure/{dag_id}: the grid's
// left-column task list, topologically sorted.
func gridStructureHandler(specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
		if err != nil {
			handleRepoError(c, err)
			return
		}
		nodes := make([]gridNodeDTO, 0, len(spec.Tasks))
		for _, t := range topoSortTasks(spec.Tasks) {
			nodes = append(nodes, gridNodeDTO{ID: t.TaskID, Label: t.TaskID, IsMapped: false})
		}
		c.JSON(http.StatusOK, nodes)
	}
}

// structureDataHandler implements GET /ui/structure/structure_data?dag_id=: the
// graph view's nodes and edges, derived from the current spec's tasks and their
// declared dependencies.
func structureDataHandler(specs DagSpecReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Query("dag_id")
		if dagID == "" {
			AbortProblem(c, http.StatusBadRequest, "bad request", "dag_id query parameter is required")
			return
		}
		spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), dagID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		sorted := topoSortTasks(spec.Tasks)
		out := structureDataDTO{
			Nodes: make([]structureNodeDTO, 0, len(sorted)),
			Edges: make([]structureEdgeDTO, 0),
		}
		known := make(map[string]bool, len(sorted))
		for _, t := range sorted {
			known[t.TaskID] = true
		}
		for _, t := range sorted {
			op := string(t.Type)
			out.Nodes = append(out.Nodes, structureNodeDTO{
				ID: t.TaskID, Label: t.TaskID, Type: "task", Operator: &op,
			})
			deps := append([]string(nil), t.DependsOn...)
			sort.Strings(deps)
			for _, dep := range deps {
				if known[dep] {
					out.Edges = append(out.Edges, structureEdgeDTO{SourceID: dep, TargetID: t.TaskID})
				}
			}
		}
		c.JSON(http.StatusOK, out)
	}
}

// registerUIStructure mounts the topology endpoints when a spec reader is set.
func registerUIStructure(r gin.IRouter, specs DagSpecReader) {
	if specs == nil {
		return
	}
	r.GET("/ui/grid/structure/:dag_id", RequirePermission("read", "dag"), gridStructureHandler(specs))
	r.GET("/ui/structure/structure_data", RequirePermission("read", "dag"), structureDataHandler(specs))
}
