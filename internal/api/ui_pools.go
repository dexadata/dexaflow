package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// PoolStore reads and writes named task pools for the Admin UI (ADR 0053 Stage
// 3). Pools are tenant-scoped; PoolSlotUsage reports per-pool occupancy for the
// Airflow slot fields.
type PoolStore interface {
	ListPools(ctx context.Context, tenant string, limit, offset int) ([]domain.Pool, int, error)
	GetPool(ctx context.Context, tenant, name string) (domain.Pool, error)
	SetPool(ctx context.Context, tenant string, p domain.Pool) error
	DeletePool(ctx context.Context, tenant, name string) error
	PoolSlotUsage(ctx context.Context, tenant string) (map[string]domain.PoolUsage, error)
}

// poolDTO is the Airflow 3.2.1 PoolResponse. occupied_slots are the slots the
// admission gate actually spends (queued+running); open_slots is the remaining
// headroom. scheduled/deferred are reported for the UI but hold no slot.
type poolDTO struct {
	Name            string  `json:"name"`
	Slots           int     `json:"slots"`
	OccupiedSlots   int     `json:"occupied_slots"`
	RunningSlots    int     `json:"running_slots"`
	QueuedSlots     int     `json:"queued_slots"`
	ScheduledSlots  int     `json:"scheduled_slots"`
	OpenSlots       int     `json:"open_slots"`
	DeferredSlots   int     `json:"deferred_slots"`
	Description     *string `json:"description"`
	IncludeDeferred bool    `json:"include_deferred"`
}

type poolCollectionDTO struct {
	Pools        []poolDTO `json:"pools"`
	TotalEntries int       `json:"total_entries"`
}

func toPoolDTO(p domain.Pool, u domain.PoolUsage) poolDTO {
	occupied := u.Running + u.Queued
	open := p.Slots - occupied
	if open < 0 {
		open = 0
	}
	return poolDTO{
		Name: p.Name, Slots: p.Slots,
		OccupiedSlots: occupied, RunningSlots: u.Running, QueuedSlots: u.Queued,
		ScheduledSlots: u.Scheduled, OpenSlots: open, DeferredSlots: u.Deferred,
		Description: strPtrOrNil(p.Description), IncludeDeferred: false,
	}
}

// poolBody is the POST/PATCH payload. Slots is a pointer so a PATCH that omits it
// keeps the pool's current cap. include_deferred is accepted for Airflow-client
// compatibility but not persisted (Dexaflow has no deferred-slot accounting).
type poolBody struct {
	Name            string `json:"name"`
	Slots           *int   `json:"slots"`
	Description     string `json:"description"`
	IncludeDeferred bool   `json:"include_deferred"`
}

// poolUsage loads the tenant's per-pool occupancy. With confine (pools locked by
// server.pools_read_only) the scheduler admits a task that names a pool the
// tenant has not defined against default_pool, so its occupancy is folded into
// default_pool here too, keeping the Pools screen in step with admission.
func poolUsage(ctx context.Context, store PoolStore, tenant string, confine bool) (map[string]domain.PoolUsage, error) {
	usage, err := store.PoolSlotUsage(ctx, tenant)
	if err != nil || !confine {
		return usage, err
	}
	for name, u := range usage {
		if name == domain.DefaultPoolName {
			continue
		}
		_, gerr := store.GetPool(ctx, tenant, name)
		if errors.Is(gerr, domain.ErrNotFound) {
			d := usage[domain.DefaultPoolName]
			d.Running += u.Running
			d.Queued += u.Queued
			d.Scheduled += u.Scheduled
			d.Deferred += u.Deferred
			usage[domain.DefaultPoolName] = d
			delete(usage, name)
			continue
		}
		if gerr != nil {
			return nil, gerr
		}
	}
	return usage, nil
}

func listPoolsHandler(store PoolStore, confine bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, offset := pagination(c)
		pools, total, err := store.ListPools(c.Request.Context(), tenantOf(c), limit, offset)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		usage, err := poolUsage(c.Request.Context(), store, tenantOf(c), confine)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		out := poolCollectionDTO{Pools: make([]poolDTO, 0, len(pools)), TotalEntries: total}
		for _, p := range pools {
			out.Pools = append(out.Pools, toPoolDTO(p, usage[p.Name]))
		}
		c.JSON(http.StatusOK, out)
	}
}

func getPoolHandler(store PoolStore, confine bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("pool_name")
		pool, err := store.GetPool(c.Request.Context(), tenantOf(c), name)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		usage, err := poolUsage(c.Request.Context(), store, tenantOf(c), confine)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, toPoolDTO(pool, usage[name]))
	}
}

func createPoolHandler(store PoolStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body poolBody
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if body.Name == "" || body.Slots == nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", "name and slots are required")
			return
		}
		pool := domain.Pool{Name: body.Name, Slots: *body.Slots, Description: body.Description}
		if err := store.SetPool(c.Request.Context(), tenantOf(c), pool); err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusCreated, toPoolDTO(pool, domain.PoolUsage{}))
	}
}

func updatePoolHandler(store PoolStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("pool_name")
		existing, err := store.GetPool(c.Request.Context(), tenantOf(c), name)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		var body poolBody
		if err = c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		// PATCH keeps the existing cap when slots is omitted; description is
		// overwritten by whatever the body carries (Airflow's PATCH semantics).
		pool := domain.Pool{Name: name, Slots: existing.Slots, Description: body.Description}
		if body.Slots != nil {
			pool.Slots = *body.Slots
		}
		if err = store.SetPool(c.Request.Context(), tenantOf(c), pool); err != nil {
			handleRepoError(c, err)
			return
		}
		usage, uerr := store.PoolSlotUsage(c.Request.Context(), tenantOf(c))
		if uerr != nil {
			handleRepoError(c, uerr)
			return
		}
		c.JSON(http.StatusOK, toPoolDTO(pool, usage[name]))
	}
}

func deletePoolHandler(store PoolStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := store.DeletePool(c.Request.Context(), tenantOf(c), c.Param("pool_name")); err != nil {
			handleRepoError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// PoolsReadOnlyDetail is the stable problem detail of the 403 every pool write
// gets when server.pools_read_only is on. Clients may match on it to tell the
// lock apart from a missing permission.
const PoolsReadOnlyDetail = "pools are read-only on this server: their slots are managed by the platform operator"

// poolsReadOnlyHandler refuses a pool write under server.pools_read_only. It
// runs after RequirePermission, so a caller without write:pool still sees the
// permission refusal it always saw, and never reaches the store.
func poolsReadOnlyHandler(c *gin.Context) {
	AbortProblem(c, http.StatusForbidden, "forbidden", PoolsReadOnlyDetail)
}

// registerUIPools mounts the Admin Pools CRUD. Pools are Pro-only (ADR 0053): on
// Lite/non-Pro, or when no store is configured, it keeps the graceful empty
// collection so the Airflow UI's Pools screen renders instead of 404ing.
// Mutations are gated on write:pool. With readOnly (server.pools_read_only) the
// write routes stay mounted but refuse every caller, tenant admin included, so
// only the platform operator, out of band, sizes a tenant's pools.
func registerUIPools(r gin.IRouter, store PoolStore, proEnabled, readOnly bool) {
	if !proEnabled || store == nil {
		r.GET("/api/v2/pools", apiEmptyCollection("pools"))
		return
	}
	r.GET("/api/v2/pools", RequirePermission("read", "pool"), listPoolsHandler(store, readOnly))
	r.GET("/api/v2/pools/:pool_name", RequirePermission("read", "pool"), getPoolHandler(store, readOnly))
	create, update, remove := createPoolHandler(store), updatePoolHandler(store), deletePoolHandler(store)
	if readOnly {
		create, update, remove = poolsReadOnlyHandler, poolsReadOnlyHandler, poolsReadOnlyHandler
	}
	r.POST("/api/v2/pools", RequirePermission("write", "pool"), create)
	r.PATCH("/api/v2/pools/:pool_name", RequirePermission("write", "pool"), update)
	r.DELETE("/api/v2/pools/:pool_name", RequirePermission("write", "pool"), remove)
}
