package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// UsageEventReader reads the task attempt usage events of ADR 0096.
type UsageEventReader interface {
	// ReadEvents returns up to limit events after the cursor in serving
	// order, or a page that says recording is off or that the cursor is
	// older than the events kept.
	ReadEvents(ctx context.Context, after domain.UsageCursor, limit int) (domain.UsagePage, error)
}

// Page sizes of GET /api/v2/service/usage.
const (
	usageDefaultLimit = 500
	usageMaxLimit     = 5000
)

// usageCursorWire is a usage cursor's encoded form: the writing transaction's
// id, as a string so no client rounds it, and the event id.
type usageCursorWire struct {
	Xid string `json:"x"`
	ID  int64  `json:"i"`
}

func encodeUsageCursor(c domain.UsageCursor) string {
	b, _ := json.Marshal(usageCursorWire{Xid: strconv.FormatUint(c.Xid, 10), ID: c.ID}) //nolint:errcheck // a string and an int always marshal
	return base64.RawURLEncoding.EncodeToString(b)
}

var errBadUsageCursor = errors.New("after is not a cursor this server issued")

func decodeUsageCursor(raw string) (domain.UsageCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return domain.UsageCursor{}, errBadUsageCursor
	}
	var w usageCursorWire
	if jerr := json.Unmarshal(b, &w); jerr != nil || w.ID < 0 {
		return domain.UsageCursor{}, errBadUsageCursor
	}
	xid, err := strconv.ParseUint(w.Xid, 10, 64)
	if err != nil {
		return domain.UsageCursor{}, errBadUsageCursor
	}
	return domain.UsageCursor{Xid: xid, ID: w.ID}, nil
}

// usageEventJSON is one event of the usage route. The attempt is
// (task_instance_id, try_number, attempt_epoch); with revision it is the key
// a consumer dedupes on, and the latest revision is the attempt's state.
type usageEventJSON struct {
	Cursor          string     `json:"cursor"`
	Tenant          string     `json:"tenant"`
	DagID           string     `json:"dag_id"`
	RunID           string     `json:"run_id"`
	TaskInstanceID  string     `json:"task_instance_id"`
	TaskID          string     `json:"task_id"`
	MapIndex        int        `json:"map_index"`
	TryNumber       int        `json:"try_number"`
	AttemptEpoch    int        `json:"attempt_epoch"`
	Revision        int        `json:"revision"`
	State           string     `json:"state"`
	PreviousState   string     `json:"previous_state"`
	PoolSlots       int        `json:"pool_slots"`
	StartedAt       *time.Time `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	LastFailureKind string     `json:"last_failure_kind,omitempty"`
	RecordedAt      time.Time  `json:"recorded_at"`
}

func newUsageEventJSON(e domain.UsageEvent) usageEventJSON {
	return usageEventJSON{
		Cursor: encodeUsageCursor(e.Cursor), Tenant: e.Tenant, DagID: e.DagID, RunID: e.RunID,
		TaskInstanceID: e.TaskInstanceID, TaskID: e.TaskID, MapIndex: e.MapIndex, TryNumber: e.TryNumber,
		AttemptEpoch: e.AttemptEpoch, Revision: e.Revision, State: string(e.State), PreviousState: string(e.PreviousState),
		PoolSlots: e.PoolSlots, StartedAt: utcPtr(e.StartedAt), EndedAt: utcPtr(e.EndedAt),
		LastFailureKind: e.LastFailureKind, RecordedAt: e.RecordedAt.UTC(),
	}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// usageGoneProblem is the 410 of the usage route: a problem that also names
// the oldest cursor whose following events are all kept.
type usageGoneProblem struct {
	Problem
	OldestCursor string `json:"oldest_cursor"`
}

// usageEventsHandler implements GET /api/v2/service/usage?after=&limit=
// (ADR 0096 point 4): the usage events after the cursor, in an order where
// every event written later sorts after them, so a consumer that stores the
// returned cursor and dedupes on the attempt and revision misses nothing.
func usageEventsHandler(deps Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := usageDefaultLimit
		if raw := c.Query("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > usageMaxLimit {
				AbortProblem(c, http.StatusBadRequest, "bad request", "limit must be a whole number from 1 to "+strconv.Itoa(usageMaxLimit))
				return
			}
			limit = n
		}
		rawAfter := c.Query("after")
		var after domain.UsageCursor
		if rawAfter != "" {
			var err error
			if after, err = decodeUsageCursor(rawAfter); err != nil {
				AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
				return
			}
		}
		page, err := deps.Issuers.ServiceUsage.ReadEvents(c.Request.Context(), after, limit)
		switch {
		case err != nil:
			AbortProblemCause(c, http.StatusInternalServerError, "internal error", "could not read the usage events", err)
			return
		case !page.Enabled:
			AbortProblem(c, http.StatusNotFound, "not found", "usage events are not recorded: usage.events.enabled is off")
			return
		case page.Gone:
			detail := "events after this cursor were deleted by retention; resume from oldest_cursor"
			c.Set(contextKeyProblemDetail, detail)
			c.Header("Content-Type", "application/problem+json")
			c.AbortWithStatusJSON(http.StatusGone, usageGoneProblem{
				Problem:      Problem{Type: "about:blank", Title: "gone", Status: http.StatusGone, Detail: detail, Instance: c.Request.URL.Path},
				OldestCursor: encodeUsageCursor(page.Oldest),
			})
			return
		}
		events := make([]usageEventJSON, len(page.Events))
		next := rawAfter
		for i, e := range page.Events {
			events[i] = newUsageEventJSON(e)
			next = events[i].Cursor
		}
		c.JSON(http.StatusOK, gin.H{"events": events, "next_cursor": next, "has_more": len(events) == limit})
	}
}
