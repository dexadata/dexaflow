package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// AuditLogReader lists recorded actions for the Audit Log view. dagID == ""
// means no DAG filter.
type AuditLogReader interface {
	ListAuditLogs(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error)
}

// eventLogDTO is the Airflow 3.2.1 EventLogResponse. Dexaflow's audit_log records
// actions against resources; task/run/map fields are null (we audit at the DAG
// and user level), and dag_id is set only for dag-scoped events.
type eventLogDTO struct {
	EventLogID      int64      `json:"event_log_id"`
	When            time.Time  `json:"when"`
	DagID           *string    `json:"dag_id"`
	TaskID          *string    `json:"task_id"`
	RunID           *string    `json:"run_id"`
	MapIndex        *int       `json:"map_index"`
	TryNumber       *int       `json:"try_number"`
	Event           string     `json:"event"`
	LogicalDate     *time.Time `json:"logical_date"`
	Owner           *string    `json:"owner"`
	Extra           *string    `json:"extra"`
	DagDisplayName  *string    `json:"dag_display_name"`
	TaskDisplayName *string    `json:"task_display_name"`
}

type eventLogCollectionDTO struct {
	EventLogs    []eventLogDTO `json:"event_logs"`
	TotalEntries int           `json:"total_entries"`
}

// toEventLogDTO maps an audit entry onto the EventLogResponse shape.
func toEventLogDTO(e domain.AuditLogEntry) eventLogDTO {
	dto := eventLogDTO{
		EventLogID: e.ID,
		When:       e.When,
		Event:      e.Action,
		Owner:      strPtrOrNil(e.Owner),
	}
	if e.ResourceType == "dag" && e.ResourceID != "" {
		dag := e.ResourceID
		dto.DagID = &dag
		dto.DagDisplayName = &dag
	}
	// metadata is a JSON object; surface run_id into its own column and keep
	// `extra` only for whatever remains (an empty {} renders as null, not braces).
	var meta map[string]any
	if e.Extra != "" && json.Unmarshal([]byte(e.Extra), &meta) == nil {
		if rid, ok := meta["run_id"].(string); ok && rid != "" {
			dto.RunID = &rid
		}
		if tid, ok := meta["task_id"].(string); ok && tid != "" {
			dto.TaskID = &tid
			dto.TaskDisplayName = &tid
		}
		if tn, ok := meta["try_number"].(float64); ok { // JSON numbers decode as float64
			n := int(tn)
			dto.TryNumber = &n
		}
		if mi, ok := meta["map_index"].(float64); ok {
			n := int(mi)
			dto.MapIndex = &n
		}
		// Whatever remains beyond the promoted keys stays as `extra`; an empty
		// object renders as null rather than literal braces.
		for _, k := range []string{"run_id", "task_id", "try_number", "map_index"} {
			delete(meta, k)
		}
		if len(meta) > 0 {
			if rest, err := json.Marshal(meta); err == nil {
				s := string(rest)
				dto.Extra = &s
			}
		}
	}
	return dto
}

// eventLogsHandler implements GET /api/v2/eventLogs (and the per-DAG Audit Log
// tab via ?dag_id=). limit defaults to 50, capped at 1000 and at
// server.max_page_limit when one is set. An optional cursor
// replaces offset with keyset paging (see keyset_pagination.go).
func eventLogsHandler(reader AuditLogReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := capPageLimit(c, clampLimit(c.Query("limit"), 50, 1000))
		if raw := c.Query("cursor"); raw != "" {
			listAuditLogsByCursor(c, reader, raw, limit)
			return
		}
		offset := atoiOr(c.Query("offset"), 0)
		entries, total, err := reader.ListAuditLogs(c.Request.Context(), tenantOf(c), c.Query("dag_id"), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := eventLogCollectionDTO{EventLogs: make([]eventLogDTO, 0, len(entries)), TotalEntries: total}
		for _, e := range entries {
			out.EventLogs = append(out.EventLogs, toEventLogDTO(e))
		}
		if _, ok := reader.(AuditLogPageReader); ok && len(entries) > 0 && offset+len(entries) < total {
			setNextCursor(c, auditCursor(entries[len(entries)-1]), false)
		}
		c.JSON(http.StatusOK, out)
	}
}

// clampLimit parses a limit query value, falling back to def and capping at upper.
func clampLimit(raw string, def, upper int) int {
	n := atoiOr(raw, def)
	if n <= 0 {
		n = def
	}
	if n > upper {
		n = upper
	}
	return n
}

// atoiOr parses an int, returning def on any error.
func atoiOr(raw string, def int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	return n
}

// registerUIAudit mounts the Audit Log endpoint. With a reader it serves real
// audit entries; without one it falls back to a schema-valid empty collection so
// the UI's Audit Log tab still renders.
func registerUIAudit(r gin.IRouter, reader AuditLogReader) {
	if reader == nil {
		r.GET("/api/v2/eventLogs", apiEmptyCollection("event_logs"))
		return
	}
	r.GET("/api/v2/eventLogs", RequirePermission("read", "audit_log"), eventLogsHandler(reader))
}
