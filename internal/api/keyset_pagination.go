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
// list; the query that reads it stays tenant and DAG scoped. A keyset cursor
// carries t and k; an offset cursor carries only o. A keyset cursor issued for
// a response body (see cursorMode) also carries o, the offset of the page it opens,
// so that page can name its previous page. A keyset cursor also carries n,
// the list's total when it was issued, so the page it opens skips the COUNT;
// a cursor without it is still accepted and that page counts.
type cursorWire struct {
	At     string `json:"t,omitempty"`
	Key    string `json:"k,omitempty"`
	Offset *int   `json:"o,omitempty"`
	Total  int    `json:"n,omitempty"`
}

func encodeCursor(cur domain.PageCursor) string {
	return encodeWire(cursorWire{At: cur.At.UTC().Format(time.RFC3339Nano), Key: cur.Key, Total: cur.Total})
}

func encodeWire(w cursorWire) string {
	b, _ := json.Marshal(w) //nolint:errcheck // strings and an int always marshal
	return base64.RawURLEncoding.EncodeToString(b)
}

// encodeOffsetCursor is the cursor of the page that starts at offset.
func encodeOffsetCursor(offset int) string {
	return encodeWire(cursorWire{Offset: &offset})
}

// encodeKeysetCursorAt is a keyset cursor that also records the offset of the
// page it opens.
func encodeKeysetCursorAt(cur domain.PageCursor, offset int) string {
	return encodeWire(cursorWire{At: cur.At.UTC().Format(time.RFC3339Nano), Key: cur.Key, Offset: &offset, Total: cur.Total})
}

// listCursor is a decoded body cursor: a keyset position, an offset, or both.
type listCursor struct {
	// Keyset is the position to read after; nil for an offset cursor.
	Keyset *domain.PageCursor
	// Offset is the offset of the page the cursor opens, or -1 when the
	// cursor does not record it (a keyset cursor from the response header).
	Offset int
}

// decodeListCursor accepts every cursor this server issues.
func decodeListCursor(raw string) (listCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return listCursor{}, errBadCursor
	}
	var w cursorWire
	if jerr := json.Unmarshal(b, &w); jerr != nil {
		return listCursor{}, errBadCursor
	}
	if w.At == "" && w.Key == "" {
		if w.Offset == nil || *w.Offset < 0 {
			return listCursor{}, errBadCursor
		}
		return listCursor{Offset: *w.Offset}, nil
	}
	cur, err := decodeCursor(raw)
	if err != nil {
		return listCursor{}, err
	}
	out := listCursor{Keyset: &cur, Offset: -1}
	if w.Offset != nil && *w.Offset >= 0 {
		out.Offset = *w.Offset
	}
	return out, nil
}

// cursorMode reports whether the request pages by cursor, the way the Airflow
// 3.3 UI pages the runs and task instance tables: it always sends `cursor`
// (empty on the first page), never an offset, and moves with the
// next_cursor and previous_cursor the body names. Clients that send no cursor
// get the body they always got, without those fields.
func cursorMode(c *gin.Context) bool {
	return c.Request.URL.Query().Has("cursor")
}

// previousOffsetCursor is the body cursor of the page before the one at
// offset, or nil on the first page or when the offset is unknown.
func previousOffsetCursor(offset, limit int) *string {
	if offset <= 0 {
		return nil
	}
	s := encodeOffsetCursor(max(0, offset-limit))
	return &s
}

var errBadCursor = errors.New("cursor is not one this server issued")

func decodeCursor(raw string) (domain.PageCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return domain.PageCursor{}, errBadCursor
	}
	var w cursorWire
	if jerr := json.Unmarshal(b, &w); jerr != nil || w.At == "" || w.Key == "" || w.Total < 0 {
		return domain.PageCursor{}, errBadCursor
	}
	at, err := time.Parse(time.RFC3339Nano, w.At)
	if err != nil {
		return domain.PageCursor{}, errBadCursor
	}
	return domain.PageCursor{At: at, Key: w.Key, Total: w.Total}, nil
}

// runCursor is the cursor after r in a list of total runs.
func runCursor(r domain.DagRun, total int) domain.PageCursor {
	return domain.PageCursor{At: r.LogicalDate, Key: r.RunID, Total: total}
}

// auditCursor is the cursor after e in a list of total entries.
func auditCursor(e domain.AuditLogEntry, total int) domain.PageCursor {
	return domain.PageCursor{At: e.When, Key: strconv.FormatInt(e.ID, 10), Total: total}
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

// listDagRunsByCursor serves one keyset page of a DAG's runs. offset is the
// page's offset when the cursor recorded it, else -1.
func listDagRunsByCursor(c *gin.Context, repo DagRunRepository, versions DagVersionLister, cur domain.PageCursor, offset int, states []string, limit int) {
	pr, ok := repo.(DagRunPageReader)
	if !ok {
		AbortProblem(c, http.StatusBadRequest, "bad request", "cursor pagination is not available for this list")
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
	pinned := pinnedDagVersions(c, versions)
	for _, r := range runs {
		dto := toDagRunDTO(r)
		pinned.name(&dto, r)
		out.DagRuns = append(out.DagRuns, dto)
	}
	if more {
		last := runCursor(runs[len(runs)-1], total)
		setNextCursor(c, last, true)
		next := encodeCursor(last)
		if offset >= 0 {
			next = encodeKeysetCursorAt(last, offset+len(runs))
		}
		out.NextCursor = &next
	}
	out.PreviousCursor = previousOffsetCursor(offset, limit)
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
		setNextCursor(c, auditCursor(entries[len(entries)-1], total), true)
	}
	c.JSON(http.StatusOK, out)
}
