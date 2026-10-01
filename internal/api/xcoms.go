package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/xcom"
)

// XComReader reads stored XCom values and lists a task instance's XCom keys for
// the read API.
type XComReader interface {
	GetXCom(ctx context.Context, tenant, dagID, runID, taskID, key string) (xcom.Entry, error)
	ListXComEntries(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.XComEntryMeta, error)
}

// xComEntryDTO is the Airflow-compatible XComEntry response shape.
type xComEntryDTO struct {
	Key       string          `json:"key"`
	Value     json.RawMessage `json:"value"`
	DagID     string          `json:"dag_id"`
	DagRunID  string          `json:"dag_run_id"`
	TaskID    string          `json:"task_id"`
	Timestamp time.Time       `json:"timestamp"`
	MapIndex  int             `json:"map_index"`
}

func xcomHandler(reader XComReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		serveXComValue(c, reader, c.Param("key"))
	}
}

// serveExtraLinks returns the task's operator deep-link buttons as
// {"extra_links": {name: url, ...}} (#375). The agent stores them as the reserved
// "_extra_links" XCom (computed by the runtime from operator_extra_links); an absent
// entry or nil reader yields an empty — but present — object, which the SPA's
// Object.keys(extra_links) needs to avoid crashing the Details view.
func serveExtraLinks(c *gin.Context, reader XComReader) {
	links := map[string]string{}
	if reader != nil {
		entry, err := reader.GetXCom(c.Request.Context(), tenantOf(c),
			c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), "_extra_links")
		if err == nil {
			_ = json.Unmarshal(entry.Value, &links) //nolint:errcheck // best-effort; malformed -> empty object
		}
	}
	c.JSON(http.StatusOK, gin.H{"extra_links": links})
}

// serveXComValue writes a single XCom entry (with its value). Used by the legacy
// /api/v2/xcoms/... route and the taskInstances/.../xcomEntries/{key} path.
func serveXComValue(c *gin.Context, reader XComReader, key string) {
	entry, err := reader.GetXCom(c.Request.Context(), tenantOf(c),
		c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id"), key)
	if err != nil {
		handleRepoError(c, err)
		return
	}
	c.JSON(http.StatusOK, xComEntryDTO{
		Key:       key,
		Value:     json.RawMessage(entry.Value),
		DagID:     c.Param("dag_id"),
		DagRunID:  c.Param("dag_run_id"),
		TaskID:    c.Param("task_id"),
		Timestamp: entry.CreatedAt,
		MapIndex:  -1,
	})
}

// xcomEntryMetaDTO is one Airflow 3.2.1 XComResponse in a list (no value).
type xcomEntryMetaDTO struct {
	Key             string     `json:"key"`
	Timestamp       time.Time  `json:"timestamp"`
	LogicalDate     *time.Time `json:"logical_date"`
	MapIndex        int        `json:"map_index"`
	TaskID          string     `json:"task_id"`
	DagID           string     `json:"dag_id"`
	RunID           string     `json:"run_id"`
	DagDisplayName  string     `json:"dag_display_name"`
	TaskDisplayName string     `json:"task_display_name"`
	RunAfter        time.Time  `json:"run_after"`
}

type xcomEntryCollectionDTO struct {
	XComEntries  []xcomEntryMetaDTO `json:"xcom_entries"`
	TotalEntries int                `json:"total_entries"`
}

// serveXComEntries writes the XCom list for a task instance (keys + metadata, no
// values), matching Airflow 3.2.1's XComCollectionResponse.
func serveXComEntries(c *gin.Context, reader XComReader) {
	dagID, runID, taskID := c.Param("dag_id"), c.Param("dag_run_id"), c.Param("task_id")
	entries, err := reader.ListXComEntries(c.Request.Context(), tenantOf(c), dagID, runID, taskID)
	if err != nil {
		handleRepoError(c, err)
		return
	}
	out := xcomEntryCollectionDTO{XComEntries: make([]xcomEntryMetaDTO, 0, len(entries)), TotalEntries: len(entries)}
	for _, e := range entries {
		out.XComEntries = append(out.XComEntries, xcomEntryMetaDTO{
			Key:             e.Key,
			Timestamp:       e.Timestamp,
			MapIndex:        e.MapIndex,
			TaskID:          taskID,
			DagID:           dagID,
			RunID:           runID,
			DagDisplayName:  dagID,
			TaskDisplayName: taskID,
			RunAfter:        e.Timestamp,
		})
	}
	c.JSON(http.StatusOK, out)
}
