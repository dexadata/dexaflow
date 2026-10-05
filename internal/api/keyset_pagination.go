package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// Keyset pagination (performance item 7.3). The /api/v2 list endpoints page
// by limit and offset, as Airflow 3.2 does, and keep doing so: the body shape
// and the offset parameters are unchanged. A client that wants deep pages to
// cost the same as the first passes the opaque `cursor` query parameter
// instead of `offset`. Every page that has a successor names it in the
// Dexaflow-Next-Cursor response header, offset pages included, so a client can
// switch to cursors after the first page; on cursor pages the Link header's
// next relation carries it too.

// nextCursorHeader names the cursor of the page after this one.
const nextCursorHeader = "Dexaflow-Next-Cursor"

// DagRunPageReader is the keyset form of DagRunRepository.ListDagRuns.
type DagRunPageReader interface {
	ListDagRunsAfter(ctx context.Context, tenant, dagID string, states []string, after domain.PageCursor, limit int) ([]domain.DagRun, int, error)
}

// AuditLogPageReader is the keyset form of AuditLogReader.ListAuditLogs.
type AuditLogPageReader interface {
	ListAuditLogsAfter(ctx context.Context, tenant, dagID string, after domain.PageCursor, limit int) ([]domain.AuditLogEntry, int, error)
}

// cursorWire is a cursor's encoded form. It only holds a position in the
// list; the query that reads it stays tenant and DAG scoped.
type cursorWire struct {
	At  string `json:"t"`
	Key string `json:"k"`
}

func encodeCursor(cur domain.PageCursor) string {
	b, _ := json.Marshal(cursorWire{At: cur.At.UTC().Format(time.RFC3339Nano), Key: cur.Key}) //nolint:errcheck // two strings always marshal
	return base64.RawURLEncoding.EncodeToString(b)
}

var errBadCursor = errors.New("cursor is not one this server issued")

func decodeCursor(raw string) (domain.PageCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return domain.PageCursor{}, errBadCursor
	}
	var w cursorWire
	if jerr := json.Unmarshal(b, &w); jerr != nil || w.At == "" || w.Key == "" {
		return domain.PageCursor{}, errBadCursor
	}
	at, err := time.Parse(time.RFC3339Nano, w.At)
	if err != nil {
		return domain.PageCursor{}, errBadCursor
	}
	return domain.PageCursor{At: at, Key: w.Key}, nil
}

func runCursor(r domain.DagRun) domain.PageCursor {
	return domain.PageCursor{At: r.LogicalDate, Key: r.RunID}
}

func auditCursor(e domain.AuditLogEntry) domain.PageCursor {
	return domain.PageCursor{At: e.When, Key: strconv.FormatInt(e.ID, 10)}
}

// setNextCursor names the next page's cursor in the response header and, on a
// cursor page, in the Link header's next relation with the other query
// parameters kept.
func setNextCursor(c *gin.Context, next domain.PageCursor, cursorPage bool) {
	token := encodeCursor(next)
	c.Header(nextCursorHeader, token)
	if !cursorPage {
		return
	}
	q := c.Request.URL.Query()
	q.Del("offset")
	q.Set("cursor", token)
	c.Header("Link", fmt.Sprintf(`<%s?%s>; rel="next"`, c.Request.URL.Path, q.Encode()))
}

// readCursor decodes the request's cursor, answering 400 when it is malformed
// or the store cannot page by cursor.
func readCursor(c *gin.Context, raw string, supported bool) (domain.PageCursor, bool) {
	if !supported {
		AbortProblem(c, http.StatusBadRequest, "bad request", "cursor pagination is not available for this list")
		return domain.PageCursor{}, false
	}
	cur, err := decodeCursor(raw)
	if err != nil {
		AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
		return domain.PageCursor{}, false
	}
	return cur, true
}

// listDagRunsByCursor serves one cursor page of a DAG's runs.
func listDagRunsByCursor(c *gin.Context, repo DagRunRepository, raw string, states []string, limit int) {
	pr, ok := repo.(DagRunPageReader)
	cur, ok := readCursor(c, raw, ok)
	if !ok {
		return
	}
	runs, total, err := pr.ListDagRunsAfter(c.Request.Context(), tenantOf(c), c.Param("dag_id"), states, cur, limit+1)
	if err != nil {
		handleRepoError(c, err)
		return
	}
	more := len(runs) > limit
	runs = runs[:min(len(runs), limit)]
	out := dagRunCollectionDTO{DagRuns: make([]dagRunDTO, 0, len(runs)), TotalEntries: total}
	for _, r := range runs {
		out.DagRuns = append(out.DagRuns, toDagRunDTO(r))
	}
	if more {
		setNextCursor(c, runCursor(runs[len(runs)-1]), true)
	}
	c.JSON(http.StatusOK, out)
}

// listAuditLogsByCursor serves one cursor page of the event log.
func listAuditLogsByCursor(c *gin.Context, reader AuditLogReader, raw string, limit int) {
	pr, ok := reader.(AuditLogPageReader)
	cur, ok := readCursor(c, raw, ok)
	if !ok {
		return
	}
	entries, total, err := pr.ListAuditLogsAfter(c.Request.Context(), tenantOf(c), c.Query("dag_id"), cur, limit+1)
	if err != nil {
		handleRepoError(c, err)
		return
	}
	more := len(entries) > limit
	entries = entries[:min(len(entries), limit)]
	out := eventLogCollectionDTO{EventLogs: make([]eventLogDTO, 0, len(entries)), TotalEntries: total}
	for _, e := range entries {
		out.EventLogs = append(out.EventLogs, toEventLogDTO(e))
	}
	if more {
		setNextCursor(c, auditCursor(entries[len(entries)-1]), true)
	}
	c.JSON(http.StatusOK, out)
}
