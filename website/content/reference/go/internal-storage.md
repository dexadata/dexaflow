---
title: "internal/storage"
linkTitle: "internal/storage"
weight: 7
---

```go
import "github.com/dexadata/dexaflow/internal/storage"
```

Package storage wraps the Postgres and Redis connections used by the control plane, exposing the sqlc\-generated query set and health checks.

## Index

- [func AttachRedisObservability\(ctx context.Context, r \*Redis, m RedisMetrics, interval time.Duration\) func\(\)](<#AttachRedisObservability>)
- [func NewLeaderPool\(ctx context.Context, cfg config.DatabaseSection\) \(\*pgxpool.Pool, error\)](<#NewLeaderPool>)
- [type ExecutionStore](<#ExecutionStore>)
  - [func NewExecutionStore\(pg \*Postgres\) \*ExecutionStore](<#NewExecutionStore>)
  - [func \(s \*ExecutionStore\) BindWarmAttempt\(ctx context.Context, runID, taskID string, tryNumber int, workerPod string\) error](<#ExecutionStore.BindWarmAttempt>)
  - [func \(s \*ExecutionStore\) FailTask\(ctx context.Context, taskInstanceID string, tryNumber int, reason string\) error](<#ExecutionStore.FailTask>)
  - [func \(s \*ExecutionStore\) IsTaskInstanceLive\(ctx context.Context, runID, taskID string, tryNumber int\) \(bool, error\)](<#ExecutionStore.IsTaskInstanceLive>)
  - [func \(s \*ExecutionStore\) RecordHeartbeat\(ctx context.Context, id auth.AgentIdentity\) error](<#ExecutionStore.RecordHeartbeat>)
  - [func \(s \*ExecutionStore\) ReportState\(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string\) error](<#ExecutionStore.ReportState>)
  - [func \(s \*ExecutionStore\) RequeueForRedispatch\(ctx context.Context, runID, taskID string, tryNumber int\) error](<#ExecutionStore.RequeueForRedispatch>)
  - [func \(s \*ExecutionStore\) Reschedule\(ctx context.Context, id auth.AgentIdentity, at time.Time\) error](<#ExecutionStore.Reschedule>)
  - [func \(s \*ExecutionStore\) RescheduleTask\(ctx context.Context, taskInstanceID string, tryNumber int, at time.Time\) error](<#ExecutionStore.RescheduleTask>)
  - [func \(s \*ExecutionStore\) ResolveTask\(ctx context.Context, runID, taskID string\) \(dispatch.Resolved, error\)](<#ExecutionStore.ResolveTask>)
  - [func \(s \*ExecutionStore\) SucceedTask\(ctx context.Context, taskInstanceID string, tryNumber int\) error](<#ExecutionStore.SucceedTask>)
  - [func \(s \*ExecutionStore\) TaskSpec\(ctx context.Context, id auth.AgentIdentity\) \(agentrpc.TaskSpec, error\)](<#ExecutionStore.TaskSpec>)
- [type LogReader](<#LogReader>)
  - [func NewLogReader\(pg \*Postgres, sink logs.Sink, tailer logs.Tailer\) \*LogReader](<#NewLogReader>)
  - [func \(r \*LogReader\) ReadLogs\(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int\) \(io.ReadCloser, error\)](<#LogReader.ReadLogs>)
  - [func \(r \*LogReader\) Tail\(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int\) \(lines \<\-chan string, cancel func\(\), err error\)](<#LogReader.Tail>)
- [type Postgres](<#Postgres>)
  - [func NewPostgres\(ctx context.Context, cfg config.DatabaseSection\) \(\*Postgres, error\)](<#NewPostgres>)
  - [func \(p \*Postgres\) CheckSchemaCurrent\(ctx context.Context\) error](<#Postgres.CheckSchemaCurrent>)
  - [func \(p \*Postgres\) Close\(\)](<#Postgres.Close>)
  - [func \(p \*Postgres\) Ping\(ctx context.Context\) error](<#Postgres.Ping>)
  - [func \(p \*Postgres\) SchemaVersion\(ctx context.Context\) \(version uint, dirty, exists bool, err error\)](<#Postgres.SchemaVersion>)
- [type Redis](<#Redis>)
  - [func NewRedis\(ctx context.Context, cfg config.RedisSection\) \(\*Redis, error\)](<#NewRedis>)
  - [func \(r \*Redis\) Close\(\) error](<#Redis.Close>)
  - [func \(r \*Redis\) Ping\(ctx context.Context\) error](<#Redis.Ping>)
- [type RedisMetrics](<#RedisMetrics>)
- [type Repository](<#Repository>)
  - [func NewRepository\(pg \*Postgres\) \*Repository](<#NewRepository>)
  - [func \(r \*Repository\) AddFavorite\(ctx context.Context, tenant, userID, dagID string\) error](<#Repository.AddFavorite>)
  - [func \(r \*Repository\) AlertEndpoint\(ctx context.Context, tenantID, connID string\) \(endpointURL string, headers map\[string\]string, err error\)](<#Repository.AlertEndpoint>)
  - [func \(r \*Repository\) BootstrapAdmin\(ctx context.Context, tenant, email, password string\) \(bool, error\)](<#Repository.BootstrapAdmin>)
  - [func \(r \*Repository\) BootstrapAdminHash\(ctx context.Context, tenant, email, hash string\) \(bool, error\)](<#Repository.BootstrapAdminHash>)
  - [func \(r \*Repository\) ClearDagHistory\(ctx context.Context, tenant, dagID string\) error](<#Repository.ClearDagHistory>)
  - [func \(r \*Repository\) ClearImportError\(ctx context.Context, tenant, filename string\) error](<#Repository.ClearImportError>)
  - [func \(r \*Repository\) ClearTaskInstances\(ctx context.Context, tenant, dagID, runID string, taskIDs \[\]string, onlyFailed, resetDagRun bool\) \(int, error\)](<#Repository.ClearTaskInstances>)
  - [func \(r \*Repository\) CreateDagRun\(ctx context.Context, tenant, dagID string, run domain.DagRun\) \(domain.DagRun, error\)](<#Repository.CreateDagRun>)
  - [func \(r \*Repository\) CreateOIDCUser\(ctx context.Context, tenant, email, provider, subject string, roles \[\]string\) \(\*auth.User, error\)](<#Repository.CreateOIDCUser>)
  - [func \(r \*Repository\) CreateUser\(ctx context.Context, tenant, email, password string, roles \[\]string\) \(domain.User, error\)](<#Repository.CreateUser>)
  - [func \(r \*Repository\) DagStats\(ctx context.Context, tenant string\) \(domain.DagStats, error\)](<#Repository.DagStats>)
  - [func \(r \*Repository\) DeleteConnection\(ctx context.Context, tenant, connID string\) error](<#Repository.DeleteConnection>)
  - [func \(r \*Repository\) DeleteDag\(ctx context.Context, tenant, dagID string\) error](<#Repository.DeleteDag>)
  - [func \(r \*Repository\) DeleteDagRun\(ctx context.Context, tenant, dagID, runID string\) error](<#Repository.DeleteDagRun>)
  - [func \(r \*Repository\) DeletePool\(ctx context.Context, tenant, name string\) error](<#Repository.DeletePool>)
  - [func \(r \*Repository\) DeleteVariable\(ctx context.Context, tenant, key string\) error](<#Repository.DeleteVariable>)
  - [func \(r \*Repository\) FavoriteDagIDs\(ctx context.Context, tenant, userID string\) \(map\[string\]bool, error\)](<#Repository.FavoriteDagIDs>)
  - [func \(r \*Repository\) FindUserByID\(ctx context.Context, id string\) \(\*auth.User, bool, error\)](<#Repository.FindUserByID>)
  - [func \(r \*Repository\) FindUserByLogin\(ctx context.Context, tenant, username string\) \(\*auth.User, string, error\)](<#Repository.FindUserByLogin>)
  - [func \(r \*Repository\) FindUserByOIDCSubject\(ctx context.Context, provider, subject string\) \(\*auth.User, bool, error\)](<#Repository.FindUserByOIDCSubject>)
  - [func \(r \*Repository\) GetConnection\(ctx context.Context, tenant, connID string\) \(domain.Connection, error\)](<#Repository.GetConnection>)
  - [func \(r \*Repository\) GetCurrentSpec\(ctx context.Context, tenant, dagID string\) \(domain.DAGSpec, error\)](<#Repository.GetCurrentSpec>)
  - [func \(r \*Repository\) GetDag\(ctx context.Context, tenant, dagID string\) \(domain.DAG, error\)](<#Repository.GetDag>)
  - [func \(r \*Repository\) GetDagRun\(ctx context.Context, tenant, dagID, runID string\) \(domain.DagRun, error\)](<#Repository.GetDagRun>)
  - [func \(r \*Repository\) GetPool\(ctx context.Context, tenant, name string\) \(domain.Pool, error\)](<#Repository.GetPool>)
  - [func \(r \*Repository\) GetVariable\(ctx context.Context, tenant, key string\) \(domain.Variable, error\)](<#Repository.GetVariable>)
  - [func \(r \*Repository\) HistoricalMetrics\(ctx context.Context, tenant string, since, until time.Time\) \(domain.HistoricalMetrics, error\)](<#Repository.HistoricalMetrics>)
  - [func \(r \*Repository\) LatestRunsForDags\(ctx context.Context, tenant string, dagIDs \[\]string, perDag int\) \(map\[string\]\[\]domain.DagRun, error\)](<#Repository.LatestRunsForDags>)
  - [func \(r \*Repository\) ListAuditLogs\(ctx context.Context, tenant, dagID string, limit, offset int\) \(\[\]domain.AuditLogEntry, int, error\)](<#Repository.ListAuditLogs>)
  - [func \(r \*Repository\) ListConnections\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.Connection, int, error\)](<#Repository.ListConnections>)
  - [func \(r \*Repository\) ListDagRuns\(ctx context.Context, tenant, dagID string, limit, offset int\) \(\[\]domain.DagRun, int, error\)](<#Repository.ListDagRuns>)
  - [func \(r \*Repository\) ListDagVersions\(ctx context.Context, tenant, dagID string\) \(\[\]domain.DagVersion, error\)](<#Repository.ListDagVersions>)
  - [func \(r \*Repository\) ListDags\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.DAG, int, error\)](<#Repository.ListDags>)
  - [func \(r \*Repository\) ListDagsFiltered\(ctx context.Context, tenant, runState string, paused \*bool, limit, offset int\) \(\[\]domain.DAG, int, error\)](<#Repository.ListDagsFiltered>)
  - [func \(r \*Repository\) ListImportErrors\(ctx context.Context, tenant string\) \(\[\]domain.ImportError, error\)](<#Repository.ListImportErrors>)
  - [func \(r \*Repository\) ListPools\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.Pool, int, error\)](<#Repository.ListPools>)
  - [func \(r \*Repository\) ListTaskInstanceAttempts\(ctx context.Context, tenant, dagID, runID, taskID string\) \(\[\]domain.TaskInstance, error\)](<#Repository.ListTaskInstanceAttempts>)
  - [func \(r \*Repository\) ListTaskInstances\(ctx context.Context, tenant, dagID, runID string, \_, \_ int\) \(\[\]domain.TaskInstance, int, error\)](<#Repository.ListTaskInstances>)
  - [func \(r \*Repository\) ListUsers\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.User, int, error\)](<#Repository.ListUsers>)
  - [func \(r \*Repository\) ListVariables\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.Variable, int, error\)](<#Repository.ListVariables>)
  - [func \(r \*Repository\) PoolSlotUsage\(ctx context.Context, tenant string\) \(map\[string\]domain.PoolUsage, error\)](<#Repository.PoolSlotUsage>)
  - [func \(r \*Repository\) ReconcileUserRoles\(ctx context.Context, userID string, roleNames \[\]string\) error](<#Repository.ReconcileUserRoles>)
  - [func \(r \*Repository\) RecordAuthEvent\(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map\[string\]string\) error](<#Repository.RecordAuthEvent>)
  - [func \(r \*Repository\) RecordSecretLivenessDenial\(ctx context.Context, tenantID, dagID, runID, taskID string, tryNumber int, kind, mode string\) error](<#Repository.RecordSecretLivenessDenial>)
  - [func \(r \*Repository\) RecordSecretScopeWarning\(ctx context.Context, tenantID, dagID, runID, taskID, kind string, declared, total int\) error](<#Repository.RecordSecretScopeWarning>)
  - [func \(r \*Repository\) RecordTaskActionAudit\(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int\) error](<#Repository.RecordTaskActionAudit>)
  - [func \(r \*Repository\) RecordUserCreatedAudit\(ctx context.Context, tenant, actorUserID, createdUserID, email, roles string\) error](<#Repository.RecordUserCreatedAudit>)
  - [func \(r \*Repository\) RegisterDagVersion\(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string\) \(bool, error\)](<#Repository.RegisterDagVersion>)
  - [func \(r \*Repository\) RemoveFavorite\(ctx context.Context, tenant, userID, dagID string\) error](<#Repository.RemoveFavorite>)
  - [func \(r \*Repository\) RoleExists\(ctx context.Context, tenant, role string\) \(bool, error\)](<#Repository.RoleExists>)
  - [func \(r \*Repository\) SecretConnectionURIs\(ctx context.Context, tenantID string\) \(map\[string\]string, error\)](<#Repository.SecretConnectionURIs>)
  - [func \(r \*Repository\) SecretConnectionURIsScoped\(ctx context.Context, tenantID string, names \[\]string\) \(map\[string\]string, error\)](<#Repository.SecretConnectionURIsScoped>)
  - [func \(r \*Repository\) SecretVariables\(ctx context.Context, tenantID string\) \(map\[string\]string, error\)](<#Repository.SecretVariables>)
  - [func \(r \*Repository\) SecretVariablesScoped\(ctx context.Context, tenantID string, names \[\]string\) \(map\[string\]string, error\)](<#Repository.SecretVariablesScoped>)
  - [func \(r \*Repository\) SetCipher\(c secrets.Cipher\)](<#Repository.SetCipher>)
  - [func \(r \*Repository\) SetConnection\(ctx context.Context, tenant string, c domain.Connection\) error](<#Repository.SetConnection>)
  - [func \(r \*Repository\) SetConnectionPatch\(ctx context.Context, tenant string, p domain.ConnectionPatch\) error](<#Repository.SetConnectionPatch>)
  - [func \(r \*Repository\) SetDagRunState\(ctx context.Context, tenant, dagID, runID, state string\) error](<#Repository.SetDagRunState>)
  - [func \(r \*Repository\) SetExternalSecretCoverage\(c externalSecretCoverage\)](<#Repository.SetExternalSecretCoverage>)
  - [func \(r \*Repository\) SetImportError\(ctx context.Context, tenant string, e domain.ImportError\) error](<#Repository.SetImportError>)
  - [func \(r \*Repository\) SetPaused\(ctx context.Context, tenant, dagID string, paused bool\) \(domain.DAG, error\)](<#Repository.SetPaused>)
  - [func \(r \*Repository\) SetPool\(ctx context.Context, tenant string, p domain.Pool\) error](<#Repository.SetPool>)
  - [func \(r \*Repository\) SetTaskInstanceState\(ctx context.Context, tenant, dagID, runID, taskID, state string\) error](<#Repository.SetTaskInstanceState>)
  - [func \(r \*Repository\) SetUserPassword\(ctx context.Context, tenant, email, hash string\) \(bool, error\)](<#Repository.SetUserPassword>)
  - [func \(r \*Repository\) SetVariable\(ctx context.Context, tenant string, v domain.Variable\) error](<#Repository.SetVariable>)
  - [func \(r \*Repository\) SetVariablePatch\(ctx context.Context, tenant string, p domain.VariablePatch\) error](<#Repository.SetVariablePatch>)
  - [func \(r \*Repository\) TaskInstancesForRuns\(ctx context.Context, tenant, dagID string, runIDs \[\]string\) \(\[\]domain.TaskInstance, error\)](<#Repository.TaskInstancesForRuns>)
  - [func \(r \*Repository\) TenantUUID\(ctx context.Context, name string\) \(string, error\)](<#Repository.TenantUUID>)
- [type SchedulerStore](<#SchedulerStore>)
  - [func NewSchedulerStore\(pg \*Postgres\) \*SchedulerStore](<#NewSchedulerStore>)
  - [func \(s \*SchedulerStore\) ActiveRuns\(ctx context.Context\) \(\[\]scheduler.RunState, error\)](<#SchedulerStore.ActiveRuns>)
  - [func \(s \*SchedulerStore\) ActiveWarmTargets\(ctx context.Context\) \(\[\]executor.WarmTarget, error\)](<#SchedulerStore.ActiveWarmTargets>)
  - [func \(s \*SchedulerStore\) ApplyTransition\(ctx context.Context, runID, taskID string, to domain.TaskState\) error](<#SchedulerStore.ApplyTransition>)
  - [func \(s \*SchedulerStore\) ApplyTransitions\(ctx context.Context, runID string, taskIDs \[\]string, to domain.TaskState\) error](<#SchedulerStore.ApplyTransitions>)
  - [func \(s \*SchedulerStore\) ClaimAlertAttempt\(ctx context.Context, runID string, maxAttempts int, backoff time.Duration\) \(int, error\)](<#SchedulerStore.ClaimAlertAttempt>)
  - [func \(s \*SchedulerStore\) CreateScheduledRun\(ctx context.Context, dagID string, logical time.Time\) error](<#SchedulerStore.CreateScheduledRun>)
  - [func \(s \*SchedulerStore\) FailDispatchExhausted\(ctx context.Context, runID, taskID, reason string\) error](<#SchedulerStore.FailDispatchExhausted>)
  - [func \(s \*SchedulerStore\) ListActiveStagingVolumes\(ctx context.Context\) \(\[\]domain.StagingVolumeState, error\)](<#SchedulerStore.ListActiveStagingVolumes>)
  - [func \(s \*SchedulerStore\) ListAgentLostCandidates\(ctx context.Context\) \(\[\]executor.AgentLostCandidate, error\)](<#SchedulerStore.ListAgentLostCandidates>)
  - [func \(s \*SchedulerStore\) ListBusyWarmWorkerPods\(ctx context.Context\) \(map\[string\]bool, error\)](<#SchedulerStore.ListBusyWarmWorkerPods>)
  - [func \(s \*SchedulerStore\) ListReapCandidates\(ctx context.Context\) \(\[\]executor.ReapCandidate, error\)](<#SchedulerStore.ListReapCandidates>)
  - [func \(s \*SchedulerStore\) ListRunningTasks\(ctx context.Context\) \(\[\]executor.PodLostCandidate, error\)](<#SchedulerStore.ListRunningTasks>)
  - [func \(s \*SchedulerStore\) ListStaleQueuedCandidates\(ctx context.Context\) \(\[\]executor.StaleQueuedCandidate, error\)](<#SchedulerStore.ListStaleQueuedCandidates>)
  - [func \(s \*SchedulerStore\) ListWarmBoundRunningTIs\(ctx context.Context\) \(\[\]executor.WarmBoundTI, error\)](<#SchedulerStore.ListWarmBoundRunningTIs>)
  - [func \(s \*SchedulerStore\) MarkRunAlertDelivered\(ctx context.Context, runID string, attempt int\) error](<#SchedulerStore.MarkRunAlertDelivered>)
  - [func \(s \*SchedulerStore\) MarkStagingDeleted\(ctx context.Context, pvcName, reason string\) error](<#SchedulerStore.MarkStagingDeleted>)
  - [func \(s \*SchedulerStore\) MarkTaskAgentLost\(ctx context.Context, taskInstanceID string\) \(bool, error\)](<#SchedulerStore.MarkTaskAgentLost>)
  - [func \(s \*SchedulerStore\) MarkTaskDispatchFailed\(ctx context.Context, runID, taskID, reason string\) error](<#SchedulerStore.MarkTaskDispatchFailed>)
  - [func \(s \*SchedulerStore\) MarkTaskDispatchLost\(ctx context.Context, taskInstanceID string\) error](<#SchedulerStore.MarkTaskDispatchLost>)
  - [func \(s \*SchedulerStore\) MarkTaskPodLost\(ctx context.Context, taskInstanceID string\) \(bool, error\)](<#SchedulerStore.MarkTaskPodLost>)
  - [func \(s \*SchedulerStore\) MaterializeTasks\(ctx context.Context, runID string, tasks \[\]domain.TaskSpec\) error](<#SchedulerStore.MaterializeTasks>)
  - [func \(s \*SchedulerStore\) PoolBudgets\(ctx context.Context\) \(map\[string\]int, error\)](<#SchedulerStore.PoolBudgets>)
  - [func \(s \*SchedulerStore\) ReapRun\(ctx context.Context, runID string\) error](<#SchedulerStore.ReapRun>)
  - [func \(s \*SchedulerStore\) RecordDispatchBackpressure\(ctx context.Context, runID, taskID string, nextAt time.Time\) error](<#SchedulerStore.RecordDispatchBackpressure>)
  - [func \(s \*SchedulerStore\) RecordDispatchFailure\(ctx context.Context, runID, taskID string, nextAt time.Time\) error](<#SchedulerStore.RecordDispatchFailure>)
  - [func \(s \*SchedulerStore\) RecordStagingVolume\(ctx context.Context, tenantID, dagID, runID, pvcName, size string\) error](<#SchedulerStore.RecordStagingVolume>)
  - [func \(s \*SchedulerStore\) RedispatchReschedule\(ctx context.Context, runID, taskID string\) error](<#SchedulerStore.RedispatchReschedule>)
  - [func \(s \*SchedulerStore\) ResetForInfraReplace\(ctx context.Context, runID, taskID string\) \(bool, error\)](<#SchedulerStore.ResetForInfraReplace>)
  - [func \(s \*SchedulerStore\) ResetForRetry\(ctx context.Context, runID, taskID string\) \(bool, error\)](<#SchedulerStore.ResetForRetry>)
  - [func \(s \*SchedulerStore\) ScheduledDAGs\(ctx context.Context\) \(\[\]scheduler.ScheduledDAG, error\)](<#SchedulerStore.ScheduledDAGs>)
  - [func \(s \*SchedulerStore\) SetRunState\(ctx context.Context, runID string, state domain.DagRunState\) error](<#SchedulerStore.SetRunState>)
  - [func \(s \*SchedulerStore\) SetTaskNote\(ctx context.Context, runID, taskID, note string\) error](<#SchedulerStore.SetTaskNote>)
  - [func \(s \*SchedulerStore\) SetWarmExecution\(exec config.ExecutionSection\)](<#SchedulerStore.SetWarmExecution>)
- [type XComIndex](<#XComIndex>)
  - [func NewXComIndex\(pg \*Postgres\) \*XComIndex](<#NewXComIndex>)
  - [func \(x \*XComIndex\) PurgeExpired\(ctx context.Context\) error](<#XComIndex.PurgeExpired>)
  - [func \(x \*XComIndex\) RecordXCom\(ctx context.Context, e xcom.IndexEntry\) error](<#XComIndex.RecordXCom>)
- [type XComReader](<#XComReader>)
  - [func NewXComReader\(pg \*Postgres, backend xcom.Backend\) \*XComReader](<#NewXComReader>)
  - [func \(r \*XComReader\) GetXCom\(ctx context.Context, tenant, dagID, runID, taskID, key string\) \(xcom.Entry, error\)](<#XComReader.GetXCom>)
  - [func \(r \*XComReader\) ListXComEntries\(ctx context.Context, tenant, dagID, runID, taskID string\) \(\[\]domain.XComEntryMeta, error\)](<#XComReader.ListXComEntries>)


<a name="AttachRedisObservability"></a>
## func [AttachRedisObservability](<https://github.com/dexadata/leoflow/blob/main/internal/storage/redis_observability.go#L185>)

```go
func AttachRedisObservability(ctx context.Context, r *Redis, m RedisMetrics, interval time.Duration) func()
```

AttachRedisObservability registers the metrics hook and starts a goroutine that scrapes the pool stats every interval. The returned function cancels the scraper goroutine; the caller defers it \(typically the datastore cleanup chain\). Lite \(Redis nil\) is a no\-op.

The scraper is a closure so the "last seen cumulative timeouts" counter \(used to compute per\-scrape DELTAS for the Prometheus counter, since go\-redis exposes Timeouts as a cumulative value\) is goroutine\-local — no shared mutable state.

<a name="NewLeaderPool"></a>
## func [NewLeaderPool](<https://github.com/dexadata/leoflow/blob/main/internal/storage/postgres.go#L118>)

```go
func NewLeaderPool(ctx context.Context, cfg config.DatabaseSection) (*pgxpool.Pool, error)
```

NewLeaderPool opens a dedicated single\-connection pool for the scheduler advisory lock, so the session holding the lock is stable \(ADR 0009\).

<a name="ExecutionStore"></a>
## type [ExecutionStore](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L20-L23>)

ExecutionStore resolves task execution context from Postgres. It implements both agentrpc.Store \(serving the in\-pod agent\) and dispatch.Resolver \(feeding the pod\-path dispatcher\) over the same dag\_version spec.

```go
type ExecutionStore struct {
    // contains filtered or unexported fields
}
```

<a name="NewExecutionStore"></a>
### func [NewExecutionStore](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L26>)

```go
func NewExecutionStore(pg *Postgres) *ExecutionStore
```

NewExecutionStore builds an ExecutionStore over the given Postgres connection.

<a name="ExecutionStore.BindWarmAttempt"></a>
### func \(\*ExecutionStore\) [BindWarmAttempt](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L236>)

```go
func (s *ExecutionStore) BindWarmAttempt(ctx context.Context, runID, taskID string, tryNumber int, workerPod string) error
```

BindWarmAttempt records the durable warm\-attempt binding \(ADR 0058 N1d\-a1\): the warm worker pod \(workerPod, its own downward\-API pod name\) that acked this attempt as started, stamped onto warm\_worker\_id so a later failover reaper can tell which running attempts a dead warm pod held. The UPDATE is guarded on state IN \('queued', 'running'\), so a settled attempt is never bound — an ack that races a reaper settling the row is a benign no\-op \(zero rows\), not an error. It is written ONLY on a warm ack; a dedicated\-pod attempt \(and every attempt while warm pools are off\) leaves warm\_worker\_id NULL.

N1d\-a2 deferral: warm\_worker\_id is intentionally NOT cleared when the attempt settles. The consuming reaper filters on state, so a lingering value on a terminal TI is harmless; a settle\-time clear is left to that increment.

<a name="ExecutionStore.FailTask"></a>
### func \(\*ExecutionStore\) [FailTask](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L281>)

```go
func (s *ExecutionStore) FailTask(ctx context.Context, taskInstanceID string, tryNumber int, reason string) error
```

FailTask marks a task instance failed by its ID, guarded by the attempt \(try\_number\) and the active states so it never clobbers a different attempt or a terminal row. It implements part of executor.OutcomeReporter for the pod reconciler \(ADR 0052\).

<a name="ExecutionStore.IsTaskInstanceLive"></a>
### func \(\*ExecutionStore\) [IsTaskInstanceLive](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L265>)

```go
func (s *ExecutionStore) IsTaskInstanceLive(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
```

IsTaskInstanceLive reports whether the attempt \(runID, taskID, tryNumber\) is still live — present and in an active \(non\-terminal\) state — derived from the same predicate RecordHeartbeat writes on, but as a pure read with no side\-effect \(ADR 0055\). It is the read\-only revocation signal the secret path consults: a terminal, superseded \(try\_number moved on\), or reaped attempt is not live, so its token stops resolving secrets even while the signature holds.

It derives ONLY from \(run, task, try\) \+ active state, exactly as the heartbeat predicate does. It must never gain a run\-recency / logical\_date clause: a recency term would deny a legitimate clear\-and\-rerun of an old run, binding credential lifetime to the run's age rather than to the attempt.

<a name="ExecutionStore.RecordHeartbeat"></a>
### func \(\*ExecutionStore\) [RecordHeartbeat](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L200>)

```go
func (s *ExecutionStore) RecordHeartbeat(ctx context.Context, id auth.AgentIdentity) error
```

RecordHeartbeat stamps last\_heartbeat\_at on the agent's TI so the scheduler's heartbeat reaper \(\#128\) can tell a live task from one whose agent has gone silent. The SQL guard skips terminal rows — a late heartbeat after a terminal report is a no\-op, not a regression.

<a name="ExecutionStore.ReportState"></a>
### func \(\*ExecutionStore\) [ReportState](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L151>)

```go
func (s *ExecutionStore) ReportState(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string) error
```

ReportState records a state transition reported by the agent, persisting the exit code and error message and stamping started/ended/duration timestamps.

<a name="ExecutionStore.RequeueForRedispatch"></a>
### func \(\*ExecutionStore\) [RequeueForRedispatch](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L333>)

```go
func (s *ExecutionStore) RequeueForRedispatch(ctx context.Context, runID, taskID string, tryNumber int) error
```

RequeueForRedispatch re\-places a reclaimed warm assignment \(ADR 0058 N1d\-c, H2\): an attempt that a warm worker was handed but demonstrably will not run \(its stream ended holding an unacked lease, or it acked started=false\) sits \`queued\` having never run. This moves it back to \`scheduled\` so the planner re\-admits and re\-dispatches it, rather than leaving it stuck until the 3\-minute dispatch\-lost reaper. Guarded to state='queued' and bounded to the exact attempt \(runID, taskID, tryNumber\): zero rows is the guard working \(the TI is running or settled, or the attempt moved on\), a benign no\-op, not an error. It bumps neither try\_number nor infra\_attempts — the attempt never ran, this is a re\-offer of the same attempt.

<a name="ExecutionStore.Reschedule"></a>
### func \(\*ExecutionStore\) [Reschedule](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L184>)

```go
func (s *ExecutionStore) Reschedule(ctx context.Context, id auth.AgentIdentity, at time.Time) error
```

Reschedule parks an active task instance in up\_for\_reschedule with its next\-poke time, so the scheduler re\-dispatches it once reschedule\_at passes \(\#380\). Used by the agent's reschedule path; a no\-op if the TI is no longer active \(terminal\).

<a name="ExecutionStore.RescheduleTask"></a>
### func \(\*ExecutionStore\) [RescheduleTask](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L311>)

```go
func (s *ExecutionStore) RescheduleTask(ctx context.Context, taskInstanceID string, tryNumber int, at time.Time) error
```

RescheduleTask parks a task instance in up\_for\_reschedule with the recovered next\-poke time, guarded by the attempt and the active states, consuming no retry budget \(ADR 0052\). Used by the reconciler when a reschedule report was lost.

<a name="ExecutionStore.ResolveTask"></a>
### func \(\*ExecutionStore\) [ResolveTask](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L347>)

```go
func (s *ExecutionStore) ResolveTask(ctx context.Context, runID, taskID string) (dispatch.Resolved, error)
```

ResolveTask returns the dispatcher's execution context for a run's task.

<a name="ExecutionStore.SucceedTask"></a>
### func \(\*ExecutionStore\) [SucceedTask](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L297>)

```go
func (s *ExecutionStore) SucceedTask(ctx context.Context, taskInstanceID string, tryNumber int) error
```

SucceedTask marks a task instance succeeded by its ID — recovering a success whose report was lost \(ADR 0052\) — guarded by the attempt and the active states. A settle on an already\-terminal or superseded row is a no\-op.

<a name="ExecutionStore.TaskSpec"></a>
### func \(\*ExecutionStore\) [TaskSpec](<https://github.com/dexadata/leoflow/blob/main/internal/storage/agent_store.go#L31>)

```go
func (s *ExecutionStore) TaskSpec(ctx context.Context, id auth.AgentIdentity) (agentrpc.TaskSpec, error)
```

TaskSpec returns the agent\-facing execution spec for a task instance.

<a name="LogReader"></a>
## type [LogReader](<https://github.com/dexadata/leoflow/blob/main/internal/storage/log_reader.go#L17-L21>)

LogReader resolves a task attempt's log location from API\-facing identifiers and reads it from the log sink, and tails its live lines.

```go
type LogReader struct {
    // contains filtered or unexported fields
}
```

<a name="NewLogReader"></a>
### func [NewLogReader](<https://github.com/dexadata/leoflow/blob/main/internal/storage/log_reader.go#L25>)

```go
func NewLogReader(pg *Postgres, sink logs.Sink, tailer logs.Tailer) *LogReader
```

NewLogReader builds a LogReader over the given Postgres connection, sink, and live\-tail tailer \(tailer may be nil to disable following\).

<a name="LogReader.ReadLogs"></a>
### func \(\*LogReader\) [ReadLogs](<https://github.com/dexadata/leoflow/blob/main/internal/storage/log_reader.go#L53>)

```go
func (r *LogReader) ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error)
```

ReadLogs resolves the run reference \(tenant name \-\> id, run\_id \-\> dag\_run id\), then opens the stored log for the task attempt. It returns domain.ErrNotFound when the run or its log file is absent. See issue \#21 for the resolution cost.

<a name="LogReader.Tail"></a>
### func \(\*LogReader\) [Tail](<https://github.com/dexadata/leoflow/blob/main/internal/storage/log_reader.go#L32>)

```go
func (r *LogReader) Tail(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (lines <-chan string, cancel func(), err error)
```

Tail subscribes to the task attempt's live log lines, returning a line channel and a cancel function. It resolves the run reference the same way ReadLogs does so the channel matches what the agent publishes.

<a name="Postgres"></a>
## type [Postgres](<https://github.com/dexadata/leoflow/blob/main/internal/storage/postgres.go#L33-L40>)

Postgres holds a pgx connection pool and the generated query set.

```go
type Postgres struct {
    Pool    *pgxpool.Pool
    Queries *queries.Queries
    // contains filtered or unexported fields
}
```

<a name="NewPostgres"></a>
### func [NewPostgres](<https://github.com/dexadata/leoflow/blob/main/internal/storage/postgres.go#L64>)

```go
func NewPostgres(ctx context.Context, cfg config.DatabaseSection) (*Postgres, error)
```

NewPostgres opens a connection pool and verifies connectivity, retrying transient failures during boot for up to pgStartupBudget. Pre\-2026\-06, the first failed ping fatal\-ed the server, so a docker compose race or any Pro failover blip became a hard crash. The retry loop keeps Lite boot ergonomic and Pro startup resilient under realistic upstream\-PG dynamics. A truly broken setup \(wrong DSN, bad auth\) still surfaces quickly because the underlying error is wrapped into the final error.

<a name="Postgres.CheckSchemaCurrent"></a>
### func \(\*Postgres\) [CheckSchemaCurrent](<https://github.com/dexadata/leoflow/blob/main/internal/storage/schema_check.go#L69>)

```go
func (p *Postgres) CheckSchemaCurrent(ctx context.Context) error
```

CheckSchemaCurrent reads the DB schema version and compares it to the binary's embedded latest, failing fast on a mismatch. Called at boot after the pool is open, before the server serves.

<a name="Postgres.Close"></a>
### func \(\*Postgres\) [Close](<https://github.com/dexadata/leoflow/blob/main/internal/storage/postgres.go#L137>)

```go
func (p *Postgres) Close()
```

Close releases the connection pool.

<a name="Postgres.Ping"></a>
### func \(\*Postgres\) [Ping](<https://github.com/dexadata/leoflow/blob/main/internal/storage/postgres.go#L132>)

```go
func (p *Postgres) Ping(ctx context.Context) error
```

Ping checks database connectivity \(used by /readyz\).

<a name="Postgres.SchemaVersion"></a>
### func \(\*Postgres\) [SchemaVersion](<https://github.com/dexadata/leoflow/blob/main/internal/storage/schema_check.go#L21>)

```go
func (p *Postgres) SchemaVersion(ctx context.Context) (version uint, dirty, exists bool, err error)
```

SchemaVersion reads the applied migration version from golang\-migrate's schema\_migrations table. exists is false when the table is absent \(migrations never ran\). It issues only a SELECT, so it is compatible with the read\-only role:api DB identity \(ADR 0049\), which skips migrations by design.

<a name="Redis"></a>
## type [Redis](<https://github.com/dexadata/leoflow/blob/main/internal/storage/redis.go#L17-L19>)

Redis wraps a go\-redis client used for XCom and locks.

```go
type Redis struct {
    Client *redis.Client
}
```

<a name="NewRedis"></a>
### func [NewRedis](<https://github.com/dexadata/leoflow/blob/main/internal/storage/redis.go#L57>)

```go
func NewRedis(ctx context.Context, cfg config.RedisSection) (*Redis, error)
```

NewRedis connects to Redis and verifies connectivity.

<a name="Redis.Close"></a>
### func \(\*Redis\) [Close](<https://github.com/dexadata/leoflow/blob/main/internal/storage/redis.go#L75>)

```go
func (r *Redis) Close() error
```

Close releases the Redis client.

<a name="Redis.Ping"></a>
### func \(\*Redis\) [Ping](<https://github.com/dexadata/leoflow/blob/main/internal/storage/redis.go#L70>)

```go
func (r *Redis) Ping(ctx context.Context) error
```

Ping checks Redis connectivity \(used by /readyz\).

<a name="RedisMetrics"></a>
## type [RedisMetrics](<https://github.com/dexadata/leoflow/blob/main/internal/storage/redis_observability.go#L18-L24>)

RedisMetrics is the subset of observability.Metrics the redis hook \+ pool scraper need. Declared as a local interface so internal/storage doesn't import internal/observability \(cycle\-avoidance, also lets tests inject a fake without standing up a Prometheus registry\).

```go
type RedisMetrics interface {
    RecordRedisCommandFailure(reason string)
    RecordRedisDialFailure(reason string)
    ObserveRedisDialDuration(d time.Duration)
    UpdateRedisPoolStats(active, idle, total uint32)
    RecordRedisPoolTimeout()
}
```

<a name="Repository"></a>
## type [Repository](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L33-L38>)

Repository implements the API resource and auth user\-store interfaces over Postgres using the sqlc\-generated query set.

```go
type Repository struct {
    // contains filtered or unexported fields
}
```

<a name="NewRepository"></a>
### func [NewRepository](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L51>)

```go
func NewRepository(pg *Postgres) *Repository
```

NewRepository builds a Repository backed by the given Postgres connection.

<a name="Repository.AddFavorite"></a>
### func \(\*Repository\) [AddFavorite](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1597>)

```go
func (r *Repository) AddFavorite(ctx context.Context, tenant, userID, dagID string) error
```

AddFavorite marks a DAG as a favorite for the user \(idempotent\).

<a name="Repository.AlertEndpoint"></a>
### func \(\*Repository\) [AlertEndpoint](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1545>)

```go
func (r *Repository) AlertEndpoint(ctx context.Context, tenantID, connID string) (endpointURL string, headers map[string]string, err error)
```

AlertEndpoint resolves an alert channel connection \(\#424\) to its endpoint for a tenant UUID: the decrypted password is the channel URL \(the full webhook URL, kept encrypted at rest\), and an optional \`headers\` object in the connection's extra becomes request headers \(e.g. an Authorization header for an endpoint whose token is not in the URL\). An absent/empty URL is an error — a misconfigured alert connection must fail loud. Never expose these in UI/API.

<a name="Repository.BootstrapAdmin"></a>
### func \(\*Repository\) [BootstrapAdmin](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1125>)

```go
func (r *Repository) BootstrapAdmin(ctx context.Context, tenant, email, password string) (bool, error)
```

BootstrapAdmin creates a default admin user with the given password when the tenant has no users yet, assigning the seeded admin role. It returns whether a user was created \(false when users already exist\).

<a name="Repository.BootstrapAdminHash"></a>
### func \(\*Repository\) [BootstrapAdminHash](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1250>)

```go
func (r *Repository) BootstrapAdminHash(ctx context.Context, tenant, email, hash string) (bool, error)
```

BootstrapAdminHash provisions the Lite admin from a precomputed bcrypt hash \(so the plaintext never reaches the control plane\). It RECONCILES: if the admin already exists, its password is reset to this hash. The Lite config \(admin\_password\_hash\) is the source of truth, so the password the setup printed always logs in — even against a pre\-existing or stale database — without anyone having to wipe Docker volumes. The only sanctioned way to change the password, \`reset\-password\`, also writes the config, so the two never drift. Returns true only when the admin was newly created \(false when an existing one was reconciled\). See cmd/dexaflow\-server bootstrapAdmin.

<a name="Repository.ClearDagHistory"></a>
### func \(\*Repository\) [ClearDagHistory](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1888>)

```go
func (r *Repository) ClearDagHistory(ctx context.Context, tenant, dagID string) error
```

ClearDagHistory deletes a DAG's runs \(cascading task instances and XCom index rows\) while keeping the DAG and its versions registered — the safe "clear" the UI trash maps to \(ADR 0020\). Returns ErrNotFound when the DAG is absent.

<a name="Repository.ClearImportError"></a>
### func \(\*Repository\) [ClearImportError](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1933>)

```go
func (r *Repository) ClearImportError(ctx context.Context, tenant, filename string) error
```

ClearImportError removes any recorded error for a file \(a good re\-import\).

<a name="Repository.ClearTaskInstances"></a>
### func \(\*Repository\) [ClearTaskInstances](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L549>)

```go
func (r *Repository) ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed, resetDagRun bool) (int, error)
```

ClearTaskInstances resets tasks to none for re\-run, optionally resetting the parent run to queued. When onlyFailed is true, only tasks currently in a failed\-ish state \(failed, upstream\_failed, up\_for\_retry\) are reset; with an empty taskIDs and onlyFailed, every failed task in the run is cleared. It returns the number of task instances actually reset.

<a name="Repository.CreateDagRun"></a>
### func \(\*Repository\) [CreateDagRun](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L436>)

```go
func (r *Repository) CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error)
```

CreateDagRun inserts a new run for a DAG at its current version. The per\-DAG max\_active\_runs cap \(\#200\) is enforced here for any caller that goes through the repository — manual triggers via the API, scripted backfills, and any future programmatic trigger path — so the contract is honored in one place. A cap of zero is treated as "unlimited" to match the scheduler path \(see \`Scheduler.hasHeadroom\`\). The check races with concurrent inserts, but the small overshoot window is bounded by the number of concurrent writers and lets us avoid an advisory lock on the hot path.

<a name="Repository.CreateOIDCUser"></a>
### func \(\*Repository\) [CreateOIDCUser](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L210>)

```go
func (r *Repository) CreateOIDCUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, error)
```

CreateOIDCUser just\-in\-time provisions an OIDC\-only account \(NULL password\), linked by \(oidc\_provider, oidc\_subject\), and grants it the given roles. It mirrors CreateUser's atomicity: every role is resolved BEFORE the insert so an unknown role fails cleanly as domain.ErrValidation without leaving an orphaned account, and the insert plus the grants run in one transaction so a failed grant rolls the account back. An empty role set grants none \(default\-deny\). A concurrent double\-provision surfaces as domain.ErrConflict via the unique \(oidc\_provider, oidc\_subject\) constraint. The returned user carries the granted role names so the caller can mint a token without a reload.

<a name="Repository.CreateUser"></a>
### func \(\*Repository\) [CreateUser](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1149>)

```go
func (r *Repository) CreateUser(ctx context.Context, tenant, email, password string, roles []string) (domain.User, error)
```

CreateUser provisions a new account in the tenant and grants it the given set of roles, returning the created user \(never its password or hash\). It reuses the same bcrypt hashing as the bootstrap admin path \(auth.HashPassword\) and the same email uniqueness guarantee, so a duplicate email surfaces as domain.ErrConflict \(the API maps it to 409\). Every role is resolved BEFORE the insert so an unknown role fails cleanly as domain.ErrValidation without leaving an orphaned account; an empty set grants none — the most restrictive default, leaving the user with no permissions until an admin grants a role.

The insert and the role grants run in a single transaction, so a failure in any grant rolls the user insert back. Without that atomicity a failed grant would leave an account the \(tenant\_id, email\) UNIQUE makes impossible to recreate — every retry would 409 forever with no recovery path.

This backs \`leoflow auth create\-user\` \(ADR 0008\) and is purely additive: it does not touch the bootstrap/reconcile path.

<a name="Repository.DagStats"></a>
### func \(\*Repository\) [DagStats](<https://github.com/dexadata/leoflow/blob/main/internal/storage/dashboard_stats.go#L16>)

```go
func (r *Repository) DagStats(ctx context.Context, tenant string) (domain.DagStats, error)
```

DagStats returns the home dashboard's DAG counters: the total active DAG count plus how many DAGs have a latest run in the failed/running/queued state.

<a name="Repository.DeleteConnection"></a>
### func \(\*Repository\) [DeleteConnection](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1861>)

```go
func (r *Repository) DeleteConnection(ctx context.Context, tenant, connID string) error
```

DeleteConnection removes a connection, returning ErrNotFound when none matched.

<a name="Repository.DeleteDag"></a>
### func \(\*Repository\) [DeleteDag](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1336>)

```go
func (r *Repository) DeleteDag(ctx context.Context, tenant, dagID string) error
```

DeleteDag removes a DAG and \(via ON DELETE CASCADE\) its versions, runs, task instances, and XCom index rows. It returns ErrNotFound when no DAG matched.

<a name="Repository.DeleteDagRun"></a>
### func \(\*Repository\) [DeleteDagRun](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L412>)

```go
func (r *Repository) DeleteDagRun(ctx context.Context, tenant, dagID, runID string) error
```

DeleteDagRun removes one run \(and, by cascade, its task instances and XCom\). It returns domain.ErrNotFound when no run with that id exists for the DAG, so the API can answer 404 rather than a silent 204 for a bad id.

<a name="Repository.DeletePool"></a>
### func \(\*Repository\) [DeletePool](<https://github.com/dexadata/leoflow/blob/main/internal/storage/pool_store.go#L68>)

```go
func (r *Repository) DeletePool(ctx context.Context, tenant, name string) error
```

DeletePool removes a pool. It returns domain.ErrNotFound when none matched and domain.ErrConflict when the target is the implicit default pool — Airflow parity: the fallback pool the gate resolves to must never be deleted.

<a name="Repository.DeleteVariable"></a>
### func \(\*Repository\) [DeleteVariable](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1680>)

```go
func (r *Repository) DeleteVariable(ctx context.Context, tenant, key string) error
```

DeleteVariable removes a variable, returning ErrNotFound when none matched.

<a name="Repository.FavoriteDagIDs"></a>
### func \(\*Repository\) [FavoriteDagIDs](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1613>)

```go
func (r *Repository) FavoriteDagIDs(ctx context.Context, tenant, userID string) (map[string]bool, error)
```

FavoriteDagIDs returns the set of DAG ids the user has favorited.

<a name="Repository.FindUserByID"></a>
### func \(\*Repository\) [FindUserByID](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L142>)

```go
func (r *Repository) FindUserByID(ctx context.Context, id string) (*auth.User, bool, error)
```

FindUserByID reloads a user's current authorization state by id: its tenant, roles, and permissions, plus whether the account is active. It is the per\- request source of truth the authenticator uses on token validation. A subject that is not a valid uuid, or that matches no row, yields auth.ErrUserNotFound \(the trusted in\-process minting path has no backing user\); any other failure is returned as\-is so the caller can fail closed.

<a name="Repository.FindUserByLogin"></a>
### func \(\*Repository\) [FindUserByLogin](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L107>)

```go
func (r *Repository) FindUserByLogin(ctx context.Context, tenant, username string) (*auth.User, string, error)
```

FindUserByLogin loads a user and its bcrypt hash for authentication.

<a name="Repository.FindUserByOIDCSubject"></a>
### func \(\*Repository\) [FindUserByOIDCSubject](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L176>)

```go
func (r *Repository) FindUserByOIDCSubject(ctx context.Context, provider, subject string) (*auth.User, bool, error)
```

FindUserByOIDCSubject resolves an OIDC identity to a Leoflow user by its immutable \(provider, subject\) pair — the trusted link key for a returning SSO login. Like FindUserByID it loads the current tenant, roles, and permissions plus the active flag, so the caller reconstructs the same principal the credential path would. A pair matching no row yields auth.ErrUserNotFound \(the signal to consider just\-in\-time provisioning\); any other failure is returned as\-is so the caller can fail closed.

<a name="Repository.GetConnection"></a>
### func \(\*Repository\) [GetConnection](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1798>)

```go
func (r *Repository) GetConnection(ctx context.Context, tenant, connID string) (domain.Connection, error)
```

GetConnection returns a connection with extra decrypted; the password is not returned \(write\-only\). Returns ErrNotFound when absent.

<a name="Repository.GetCurrentSpec"></a>
### func \(\*Repository\) [GetCurrentSpec](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L926>)

```go
func (r *Repository) GetCurrentSpec(ctx context.Context, tenant, dagID string) (domain.DAGSpec, error)
```

GetCurrentSpec returns the parsed spec of the DAG's current version, or domain.ErrNotFound if the DAG or its current version does not exist.

<a name="Repository.GetDag"></a>
### func \(\*Repository\) [GetDag](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L338>)

```go
func (r *Repository) GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error)
```

GetDag returns a single DAG by its user\-facing id.

<a name="Repository.GetDagRun"></a>
### func \(\*Repository\) [GetDagRun](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L397>)

```go
func (r *Repository) GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error)
```

GetDagRun returns a single run by its run id.

<a name="Repository.GetPool"></a>
### func \(\*Repository\) [GetPool](<https://github.com/dexadata/leoflow/blob/main/internal/storage/pool_store.go#L35>)

```go
func (r *Repository) GetPool(ctx context.Context, tenant, name string) (domain.Pool, error)
```

GetPool returns one pool by name, or domain.ErrNotFound.

<a name="Repository.GetVariable"></a>
### func \(\*Repository\) [GetVariable](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1626>)

```go
func (r *Repository) GetVariable(ctx context.Context, tenant, key string) (domain.Variable, error)
```

GetVariable returns one variable by key, or ErrNotFound.

<a name="Repository.HistoricalMetrics"></a>
### func \(\*Repository\) [HistoricalMetrics](<https://github.com/dexadata/leoflow/blob/main/internal/storage/dashboard_stats.go#L45>)

```go
func (r *Repository) HistoricalMetrics(ctx context.Context, tenant string, since, until time.Time) (domain.HistoricalMetrics, error)
```

HistoricalMetrics returns run\- and task\-instance state counts for runs whose logical date falls within \[since, until\], keyed by Leoflow state name.

<a name="Repository.LatestRunsForDags"></a>
### func \(\*Repository\) [LatestRunsForDags](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L840>)

```go
func (r *Repository) LatestRunsForDags(ctx context.Context, tenant string, dagIDs []string, perDag int) (map[string][]domain.DagRun, error)
```

LatestRunsForDags returns up to perDag most\-recent runs for each named DAG, keyed by dag\_id, in a single windowed query \(no per\-DAG round trips\).

<a name="Repository.ListAuditLogs"></a>
### func \(\*Repository\) [ListAuditLogs](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1300>)

```go
func (r *Repository) ListAuditLogs(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error)
```

ListAuditLogs returns a page of audit\-log entries for the tenant, newest first, optionally filtered to a single DAG \(dagID == "" means no filter\).

<a name="Repository.ListConnections"></a>
### func \(\*Repository\) [ListConnections](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1819>)

```go
func (r *Repository) ListConnections(ctx context.Context, tenant string, limit, offset int) ([]domain.Connection, int, error)
```

ListConnections returns a page of connections \(no passwords\) and the total.

<a name="Repository.ListDagRuns"></a>
### func \(\*Repository\) [ListDagRuns](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L376>)

```go
func (r *Repository) ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error)
```

ListDagRuns returns a page of runs for a DAG and the total count.

<a name="Repository.ListDagVersions"></a>
### func \(\*Repository\) [ListDagVersions](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L903>)

```go
func (r *Repository) ListDagVersions(ctx context.Context, tenant, dagID string) ([]domain.DagVersion, error)
```

ListDagVersions returns the DAG's versions, newest first, with a 1\-based version\_number the UI uses to query version\-scoped structure.

<a name="Repository.ListDags"></a>
### func \(\*Repository\) [ListDags](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L317>)

```go
func (r *Repository) ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error)
```

ListDags returns a page of DAGs for the tenant and the total count.

<a name="Repository.ListDagsFiltered"></a>
### func \(\*Repository\) [ListDagsFiltered](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1354>)

```go
func (r *Repository) ListDagsFiltered(ctx context.Context, tenant, runState string, paused *bool, limit, offset int) ([]domain.DAG, int, error)
```

ListDagsFiltered returns a page of active DAGs for the tenant, optionally filtered by paused state and/or latest\-run state, with the matching total. An empty runState or nil paused disables that filter.

<a name="Repository.ListImportErrors"></a>
### func \(\*Repository\) [ListImportErrors](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1900>)

```go
func (r *Repository) ListImportErrors(ctx context.Context, tenant string) ([]domain.ImportError, error)
```

ListImportErrors returns the tenant's DAG parse/compile errors, newest first.

<a name="Repository.ListPools"></a>
### func \(\*Repository\) [ListPools](<https://github.com/dexadata/leoflow/blob/main/internal/storage/pool_store.go#L12>)

```go
func (r *Repository) ListPools(ctx context.Context, tenant string, limit, offset int) ([]domain.Pool, int, error)
```

ListPools returns a page of the tenant's named pools and the total count.

<a name="Repository.ListTaskInstanceAttempts"></a>
### func \(\*Repository\) [ListTaskInstanceAttempts](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L481>)

```go
func (r *Repository) ListTaskInstanceAttempts(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.TaskInstance, error)
```

ListTaskInstanceAttempts returns every attempt for \(run, task\), oldest first — the current task\_instances row UNIONed with all archived task\_instance\_history rows. The UI's /tries endpoint needs this to render one navigable tab per attempt; without history, a cleared task shows only the latest attempt and the user cannot inspect prior failures \(Lima bug \#241\).

<a name="Repository.ListTaskInstances"></a>
### func \(\*Repository\) [ListTaskInstances](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L524>)

```go
func (r *Repository) ListTaskInstances(ctx context.Context, tenant, dagID, runID string, _, _ int) ([]domain.TaskInstance, int, error)
```

ListTaskInstances returns the task instances of a run.

<a name="Repository.ListUsers"></a>
### func \(\*Repository\) [ListUsers](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1202>)

```go
func (r *Repository) ListUsers(ctx context.Context, tenant string, limit, offset int) ([]domain.User, int, error)
```

ListUsers returns a page of the tenant's accounts, newest first, each with the full set of role names it holds. It never reads or returns password\_hash — the list must not expose secrets. The second result is the unpaged total, so the caller can render total\_entries independent of the page size.

<a name="Repository.ListVariables"></a>
### func \(\*Repository\) [ListVariables](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1382>)

```go
func (r *Repository) ListVariables(ctx context.Context, tenant string, limit, offset int) ([]domain.Variable, int, error)
```

ListVariables returns a page of variables for the tenant and the total count.

<a name="Repository.PoolSlotUsage"></a>
### func \(\*Repository\) [PoolSlotUsage](<https://github.com/dexadata/leoflow/blob/main/internal/storage/pool_store.go#L92>)

```go
func (r *Repository) PoolSlotUsage(ctx context.Context, tenant string) (map[string]domain.PoolUsage, error)
```

PoolSlotUsage returns per\-pool occupancy for the tenant, keyed by pool name \(a task instance with no pool is counted under the implicit default\_pool\). It feeds the Airflow PoolResponse occupancy fields.

<a name="Repository.ReconcileUserRoles"></a>
### func \(\*Repository\) [ReconcileUserRoles](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L279>)

```go
func (r *Repository) ReconcileUserRoles(ctx context.Context, userID string, roleNames []string) error
```

ReconcileUserRoles makes the user's granted roles exactly roleNames, atomically: it is how the identity provider stays authoritative over an OIDC user's roles. On each OIDC login the caller passes the group\-mapped role set, and this sets the DB user\_roles to precisely that set, so a demotion or deprovisioning at the IdP takes effect on the next login and the per\-request authz reload sees it.

Every name is resolved to a role id in the user's OWN tenant BEFORE any write, so a name that is not a role in that tenant fails closed as domain.ErrValidation with the prior grants untouched — the login path turns that into a rejected, audited login rather than silently wiping the user's roles. The delete and the inserts run in one transaction, making the operation idempotent \(reconciling the same set yields the same rows\) and an empty roleNames a full clear \(default\-deny\).

<a name="Repository.RecordAuthEvent"></a>
### func \(\*Repository\) [RecordAuthEvent](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L707>)

```go
func (r *Repository) RecordAuthEvent(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map[string]string) error
```

RecordAuthEvent records an authentication event to the audit log \(H5\): OIDC login success/failure, tenant\-pin rejection, JIT provisioning, break\-glass login, and logout. It NEVER records tokens or the client secret — only the actor's email, the resolved tenant \(best\-effort\), the outcome, and small non\-secret detail fields \(e.g. the rejection reason, the attempted tenant claim\). It is best\-effort: the caller logs and continues on error so a flaky audit sink never turns a security decision \(a 403 rejection, a successful login\) into a 5xx.

The event is scoped to the resolved tenant when known; events that never resolved a tenant \(a login failure, a tenant\-pin rejection\) fall back to the "default" tenant so the row still lands, with the attempted values in the metadata. resourceID carries the email so account\-scoped auth activity is filterable alongside user.create.

<a name="Repository.RecordSecretLivenessDenial"></a>
### func \(\*Repository\) [RecordSecretLivenessDenial](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L792>)

```go
func (r *Repository) RecordSecretLivenessDenial(ctx context.Context, tenantID, dagID, runID, taskID string, tryNumber int, kind, mode string) error
```

RecordSecretLivenessDenial records that the secret\-path liveness gate fired for a task instance whose attempt is no longer live \(ADR 0055\): a would\-have\-denied in observe mode, or a denial in enforce mode. It is scoped to the DAG resource so the event surfaces on the DAG's Audit Log tab, with the kind \("variables" or "connections"\), the run, task, attempt, and the gate mode in metadata. It records identity \+ kind \+ mode only — never secret names or values.

tenantID is the tenant UUID the agent token carries \(not the tenant name\): the caller is the agent RPC path, which passes AgentIdentity.TenantID. Resolving it by name silently dropped every row \(\#722\) — including enforce\-mode security denials — so mirror the secret\-delivery path and parse it as a UUID.

<a name="Repository.RecordSecretScopeWarning"></a>
### func \(\*Repository\) [RecordSecretScopeWarning](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L759>)

```go
func (r *Repository) RecordSecretScopeWarning(ctx context.Context, tenantID, dagID, runID, taskID, kind string, declared, total int) error
```

RecordSecretScopeWarning records that a task received the full tenant secret set while it declared only a narrower subset \(ADR 0045, ADR 0055\): under secret\_scoping: enforce it would receive only its declared set. It is scoped to the DAG resource so the event surfaces on the DAG's Audit Log tab, with the kind \("variables" or "connections"\), the run and task, and the declared/total counts in metadata. It records counts only — never secret names or values.

tenantID is the tenant UUID the agent token carries \(not the tenant name\): the caller is the agent RPC path, which passes AgentIdentity.TenantID. Resolving it by name silently dropped every row \(\#722\), so mirror the secret\-delivery path and parse it as a UUID.

<a name="Repository.RecordTaskActionAudit"></a>
### func \(\*Repository\) [RecordTaskActionAudit](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L636>)

```go
func (r *Repository) RecordTaskActionAudit(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error
```

RecordTaskActionAudit logs a task\-level action \(clear, mark state\) with the acting user and the run/task/try in metadata, so the Audit Log view shows the owner and the task columns. Scoped to the DAG \(resource\_id = dag\_id\) so it appears on the DAG's Audit Log tab.

<a name="Repository.RecordUserCreatedAudit"></a>
### func \(\*Repository\) [RecordUserCreatedAudit](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L667>)

```go
func (r *Repository) RecordUserCreatedAudit(ctx context.Context, tenant, actorUserID, createdUserID, email, roles string) error
```

RecordUserCreatedAudit logs an account creation with the acting admin as the owner and the new account's email and granted roles in metadata, scoped to the "user" resource so account\-management actions are visible in the Audit Log. The roles arrive as a single comma\-joined string \(empty when none were granted\).

<a name="Repository.RegisterDagVersion"></a>
### func \(\*Repository\) [RegisterDagVersion](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L944>)

```go
func (r *Repository) RegisterDagVersion(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string) (bool, error)
```

RegisterDagVersion upserts the DAG and inserts a version keyed by specHash, setting it as current. It is idempotent: an existing hash yields created=false.

<a name="Repository.RemoveFavorite"></a>
### func \(\*Repository\) [RemoveFavorite](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1605>)

```go
func (r *Repository) RemoveFavorite(ctx context.Context, tenant, userID, dagID string) error
```

RemoveFavorite clears a DAG's favorite mark for the user \(idempotent\).

<a name="Repository.RoleExists"></a>
### func \(\*Repository\) [RoleExists](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L252>)

```go
func (r *Repository) RoleExists(ctx context.Context, tenant, role string) (bool, error)
```

RoleExists reports whether a role name exists for the tenant. The OIDC login path uses it to fail closed on a misconfigured default\_role before minting a token for a returning user \(the JIT path validates roles inside CreateOIDCUser\).

<a name="Repository.SecretConnectionURIs"></a>
### func \(\*Repository\) [SecretConnectionURIs](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1436>)

```go
func (r *Repository) SecretConnectionURIs(ctx context.Context, tenantID string) (map[string]string, error)
```

SecretConnectionURIs returns the tenant's connections as conn\_id→Airflow URI \(password decrypted\), for delivering to task pods \(ADR 0021\). The agent exports them as AIRFLOW\_CONN\_\<CONN\_ID\>. tenantID is the tenant UUID carried by the agent token. Never expose these in UI/API responses.

<a name="Repository.SecretConnectionURIsScoped"></a>
### func \(\*Repository\) [SecretConnectionURIsScoped](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1504>)

```go
func (r *Repository) SecretConnectionURIsScoped(ctx context.Context, tenantID string, names []string) (map[string]string, error)
```

SecretConnectionURIsScoped returns only the named subset of the tenant's connections as Airflow URIs \(password decrypted\), filtered in the query \(ADR 0055 D1\). It backs secret\_scoping: enforce. An empty name set returns nothing without a query. Never expose these in UI/API responses. It shares the per\-connection decrypt\-and\-skip\-on\-failure semantics of SecretConnectionURIs: one undecryptable connection is skipped with a warning, never blinding the rest of the declared set.

<a name="Repository.SecretVariables"></a>
### func \(\*Repository\) [SecretVariables](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1415>)

```go
func (r *Repository) SecretVariables(ctx context.Context, tenantID string) (map[string]string, error)
```

SecretVariables returns the tenant's variables as key→value, for delivering to task pods \(ADR 0021\). The agent exports them as AIRFLOW\_VAR\_\<KEY\>. tenantID is the tenant UUID carried by the agent token \(not the tenant name\).

<a name="Repository.SecretVariablesScoped"></a>
### func \(\*Repository\) [SecretVariablesScoped](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1478>)

```go
func (r *Repository) SecretVariablesScoped(ctx context.Context, tenantID string, names []string) (map[string]string, error)
```

SecretVariablesScoped returns only the named subset of the tenant's variables, filtered in the query \(ADR 0055 D1: scope in the SQL, never post\-filter the decrypted whole vault in the handler\). It backs secret\_scoping: enforce, where a task receives only the Variables it declared. An empty name set returns nothing without a query — enforce's load\-bearing \[\] case. tenantID is the tenant UUID carried by the agent token.

<a name="Repository.SetCipher"></a>
### func \(\*Repository\) [SetCipher](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L57>)

```go
func (r *Repository) SetCipher(c secrets.Cipher)
```

SetCipher attaches the encryption cipher used for connection secrets \(ADR 0019\). Without it, connection writes fail rather than storing plaintext.

<a name="Repository.SetConnection"></a>
### func \(\*Repository\) [SetConnection](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1710>)

```go
func (r *Repository) SetConnection(ctx context.Context, tenant string, c domain.Connection) error
```

SetConnection creates or updates a connection, encrypting password and extra at rest. It fails if no encryption cipher is configured \(never stores a credential in plaintext — ADR 0019\).

<a name="Repository.SetConnectionPatch"></a>
### func \(\*Repository\) [SetConnectionPatch](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1768>)

```go
func (r *Repository) SetConnectionPatch(ctx context.Context, tenant string, p domain.ConnectionPatch) error
```

SetConnectionPatch upserts a connection with tri\-state field semantics \(\#887\): a nil field is preserved \(NULL param → COALESCE keeps the stored value\), a non\-nil field is written exactly \("" clears it\). Password and extra are encrypted when set. It is how the API write path distinguishes an omitted field from an explicit empty one without reintroducing the password\-wipe, and where a masked secret round\-trip is preserved \(the handler maps a masked password to a nil field, and unmasks extra, before calling here\). It fails if no encryption cipher is configured \(never stores a credential in plaintext — ADR 0019\).

<a name="Repository.SetDagRunState"></a>
### func \(\*Repository\) [SetDagRunState](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L611>)

```go
func (r *Repository) SetDagRunState(ctx context.Context, tenant, dagID, runID, state string) error
```

SetDagRunState sets a DAG run's state directly, backing the UI's mark run success/failed actions. Terminal states stamp ended\_at; re\-opening to a non\-terminal state clears it. started\_at is preserved.

<a name="Repository.SetExternalSecretCoverage"></a>
### func \(\*Repository\) [SetExternalSecretCoverage](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L63>)

```go
func (r *Repository) SetExternalSecretCoverage(c externalSecretCoverage)
```

SetExternalSecretCoverage attaches the operator\-configured external\-backend coverage used by the D6 registration check \(ADR 0060 B1\). Without it, the check is the pre\-0060 vault\-existence check \(a declared name must exist in the vault\). It must be wired from operator/Helm config, never from a DAG.

<a name="Repository.SetImportError"></a>
### func \(\*Repository\) [SetImportError](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1919>)

```go
func (r *Repository) SetImportError(ctx context.Context, tenant string, e domain.ImportError) error
```

SetImportError records \(or replaces\) the parse/compile error for a file.

<a name="Repository.SetPaused"></a>
### func \(\*Repository\) [SetPaused](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L351>)

```go
func (r *Repository) SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error)
```

SetPaused toggles the paused flag of a DAG.

<a name="Repository.SetPool"></a>
### func \(\*Repository\) [SetPool](<https://github.com/dexadata/leoflow/blob/main/internal/storage/pool_store.go#L52>)

```go
func (r *Repository) SetPool(ctx context.Context, tenant string, p domain.Pool) error
```

SetPool creates or updates a pool \(its slot cap and description\). The is\_default flag is not writable through this path — only the seed migration marks the implicit default pool.

<a name="Repository.SetTaskInstanceState"></a>
### func \(\*Repository\) [SetTaskInstanceState](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L821>)

```go
func (r *Repository) SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error
```

SetTaskInstanceState sets a task instance's state directly, backing the UI's "mark success"/"mark failed" actions. It does not run the task.

<a name="Repository.SetUserPassword"></a>
### func \(\*Repository\) [SetUserPassword](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1231>)

```go
func (r *Repository) SetUserPassword(ctx context.Context, tenant, email, hash string) (bool, error)
```

SetUserPassword sets a user's bcrypt hash by email, returning whether a user was updated \(false when no such user exists\). Used by \`leoflow lite reset\-password\`.

<a name="Repository.SetVariable"></a>
### func \(\*Repository\) [SetVariable](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1642>)

```go
func (r *Repository) SetVariable(ctx context.Context, tenant string, v domain.Variable) error
```

SetVariable creates or updates a variable, always overwriting the value and preserving description when empty. It is the full\-write convenience the storage tests and internal callers use; the tri\-state API write path goes through SetVariablePatch.

<a name="Repository.SetVariablePatch"></a>
### func \(\*Repository\) [SetVariablePatch](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1662>)

```go
func (r *Repository) SetVariablePatch(ctx context.Context, tenant string, p domain.VariablePatch) error
```

SetVariablePatch upserts a variable with tri\-state semantics on description \(\#887\): a nil Description is preserved \(NULL param → COALESCE\), a non\-nil one is written \("" clears it\). The \`value\` column is NOT NULL, so value cannot be preserved at the SQL layer \(a NULL param is rejected on the INSERT arm before COALESCE\); the API handler therefore resolves an omitted/masked value to the stored value before calling here, and Value is expected non\-nil \(a nil Value defensively clears to empty rather than panicking\).

<a name="Repository.TaskInstancesForRuns"></a>
### func \(\*Repository\) [TaskInstancesForRuns](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L872>)

```go
func (r *Repository) TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error)
```

TaskInstancesForRuns returns the task instances of the given runs of a DAG in one query, ordered by run\_id, task\_id, try\_number, for the grid summaries.

<a name="Repository.TenantUUID"></a>
### func \(\*Repository\) [TenantUUID](<https://github.com/dexadata/leoflow/blob/main/internal/storage/repository.go#L1404>)

```go
func (r *Repository) TenantUUID(ctx context.Context, name string) (string, error)
```

TenantUUID resolves a tenant name to its UUID string — the form the agent token carries and that the secret\-delivery methods expect.

<a name="SchedulerStore"></a>
## type [SchedulerStore](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L20-L28>)

SchedulerStore is the sqlc\-backed implementation of scheduler.Store.

```go
type SchedulerStore struct {
    // contains filtered or unexported fields
}
```

<a name="NewSchedulerStore"></a>
### func [NewSchedulerStore](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L38>)

```go
func NewSchedulerStore(pg *Postgres) *SchedulerStore
```

NewSchedulerStore builds a SchedulerStore over the given Postgres connection.

<a name="SchedulerStore.ActiveRuns"></a>
### func \(\*SchedulerStore\) [ActiveRuns](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L112>)

```go
func (s *SchedulerStore) ActiveRuns(ctx context.Context) ([]scheduler.RunState, error)
```

ActiveRuns loads every active dag run and projects it into the scheduler's RunState \(topology \+ per\-task state\), the read side of a scheduler tick.

<a name="SchedulerStore.ActiveWarmTargets"></a>
### func \(\*SchedulerStore\) [ActiveWarmTargets](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L236>)

```go
func (s *SchedulerStore) ActiveWarmTargets(ctx context.Context) ([]executor.WarmTarget, error)
```

ActiveWarmTargets returns one warm target per distinct active dag\_version with its effective warm\-worker count \(ADR 0058 N1b2b\), the read side of a warm\-pool reconcile tick. It reuses the same active\-runs \+ cached\-spec path as ActiveRuns: the spec is immutable per dag\_version\_id, so N active runs sharing a version decode it once and the effective target is derived from the DAG author's min\_idle\_workers under the operator's clamp/fallback. It implements executor.WarmTargetSource without the executor importing storage.

<a name="SchedulerStore.ApplyTransition"></a>
### func \(\*SchedulerStore\) [ApplyTransition](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L336>)

```go
func (s *SchedulerStore) ApplyTransition(ctx context.Context, runID, taskID string, to domain.TaskState) error
```

ApplyTransition moves a task instance to a new state.

<a name="SchedulerStore.ApplyTransitions"></a>
### func \(\*SchedulerStore\) [ApplyTransitions](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L354>)

```go
func (s *SchedulerStore) ApplyTransitions(ctx context.Context, runID string, taskIDs []string, to domain.TaskState) error
```

ApplyTransitions moves every listed task of a run to the SAME target state in one UPDATE, the batched equivalent of calling ApplyTransition once per task. The scheduler groups a tick's plain state\-set transitions by target state and flushes each group here, collapsing R updates into one per distinct state. The per\-row stamping is identical to the single\-row query, so the result is byte\-identical — only the statement count drops. An empty list is a no\-op.

<a name="SchedulerStore.ClaimAlertAttempt"></a>
### func \(\*SchedulerStore\) [ClaimAlertAttempt](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L508>)

```go
func (s *SchedulerStore) ClaimAlertAttempt(ctx context.Context, runID string, maxAttempts int, backoff time.Duration) (int, error)
```

ClaimAlertAttempt atomically claims one on\-failure send attempt for a run \(\#431\). The UPDATE consumes an attempt and sets the next\-attempt time, but only while the episode is undelivered, within budget, and past its backoff — see the query for why each predicate exists. pgx.ErrNoRows means the claim was refused on one of those grounds: not an error, just a lost claim, so report won=false.

Claiming an attempt is NOT the same as recording delivery; that is MarkRunAlertDelivered. Conflating the two is what made a failed send a permanently lost page. Returns the attempt number won \(0 when the claim was refused\), which the caller passes back to MarkRunAlertDelivered so a superseded send cannot stamp.

<a name="SchedulerStore.CreateScheduledRun"></a>
### func \(\*SchedulerStore\) [CreateScheduledRun](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L564>)

```go
func (s *SchedulerStore) CreateScheduledRun(ctx context.Context, dagID string, logical time.Time) error
```

CreateScheduledRun inserts a scheduled run for a DAG \(idempotent on run\_id\).

<a name="SchedulerStore.FailDispatchExhausted"></a>
### func \(\*SchedulerStore\) [FailDispatchExhausted](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L460>)

```go
func (s *SchedulerStore) FailDispatchExhausted(ctx context.Context, runID, taskID, reason string) error
```

FailDispatchExhausted fails a scheduled task as dispatch\_failed once its dispatch\-attempt budget is spent \(ADR 0031 Amendment A\).

<a name="SchedulerStore.ListActiveStagingVolumes"></a>
### func \(\*SchedulerStore\) [ListActiveStagingVolumes](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L877>)

```go
func (s *SchedulerStore) ListActiveStagingVolumes(ctx context.Context) ([]domain.StagingVolumeState, error)
```

ListActiveStagingVolumes returns active staging volumes joined with their DAG run's state \(empty when the run row is gone\), for the GC \(ADR 0022\).

<a name="SchedulerStore.ListAgentLostCandidates"></a>
### func \(\*SchedulerStore\) [ListAgentLostCandidates](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L690>)

```go
func (s *SchedulerStore) ListAgentLostCandidates(ctx context.Context) ([]executor.AgentLostCandidate, error)
```

ListAgentLostCandidates returns every \`running\` TI with a non\-null last\_heartbeat\_at, for the scheduler's TI heartbeat reaper \(\#128\). The reaper applies the threshold per row so the SQL stays simple.

<a name="SchedulerStore.ListBusyWarmWorkerPods"></a>
### func \(\*SchedulerStore\) [ListBusyWarmWorkerPods](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L806>)

```go
func (s *SchedulerStore) ListBusyWarmWorkerPods(ctx context.Context) (map[string]bool, error)
```

ListBusyWarmWorkerPods returns the set of warm\-worker pod names currently serving a \`running\` attempt \(ADR 0058 N1d\-b\): a warm worker is BUSY iff some \`running\` task\_instance is durably bound to it \(warm\_worker\_id = the pod's own name\). The busy\-aware warm\-pool reconciler reads this once per tick to classify each live warm pod as busy or idle, so scale\-down/drain deletes only IDLE workers and never kills an in\-flight attempt \(review findings M1/M2\). With warm pools off no TI is ever bound, so the set is always empty and every worker classifies as idle — byte\-for\-byte today. Implements executor.BusyWarmWorkerSource without the executor importing storage.

<a name="SchedulerStore.ListReapCandidates"></a>
### func \(\*SchedulerStore\) [ListReapCandidates](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L624>)

```go
func (s *SchedulerStore) ListReapCandidates(ctx context.Context) ([]executor.ReapCandidate, error)
```

ListReapCandidates returns every dag\_run currently in 'running' state with the timestamp of its most recent activity, for the scheduler's orphan reaper. The query \(sqlc.runs.ListOrphanCandidates\) is the authority on how to compute the timestamp; the reaper only decides whether each one is past its threshold.

<a name="SchedulerStore.ListRunningTasks"></a>
### func \(\*SchedulerStore\) [ListRunningTasks](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L837>)

```go
func (s *SchedulerStore) ListRunningTasks(ctx context.Context) ([]executor.PodLostCandidate, error)
```

ListRunningTasks returns every \`running\` TI with the timestamp it entered running, for the pod\-lost reaper \(\#527\). The reaper applies the grace period and the pod\-liveness check per row, so the SQL stays simple.

<a name="SchedulerStore.ListStaleQueuedCandidates"></a>
### func \(\*SchedulerStore\) [ListStaleQueuedCandidates](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L751>)

```go
func (s *SchedulerStore) ListStaleQueuedCandidates(ctx context.Context) ([]executor.StaleQueuedCandidate, error)
```

ListStaleQueuedCandidates returns every \`queued\` TI with its queued\_at, for the dispatch\-lost reaper \(\#202\). The reaper applies the threshold per row so the SQL stays simple.

<a name="SchedulerStore.ListWarmBoundRunningTIs"></a>
### func \(\*SchedulerStore\) [ListWarmBoundRunningTIs](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L779>)

```go
func (s *SchedulerStore) ListWarmBoundRunningTIs(ctx context.Context) ([]executor.WarmBoundTI, error)
```

ListWarmBoundRunningTIs returns every \`running\` TI durably bound to a warm worker \(warm\_worker\_id IS NOT NULL\), for the warm\-worker\-lost reaper \(ADR 0058 N1d\-a2\). With warm pools off no TI is ever bound, so this is always empty and the reaper is inert.

<a name="SchedulerStore.MarkRunAlertDelivered"></a>
### func \(\*SchedulerStore\) [MarkRunAlertDelivered](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L531>)

```go
func (s *SchedulerStore) MarkRunAlertDelivered(ctx context.Context, runID string, attempt int) error
```

MarkRunAlertDelivered stamps a run's on\-failure alert as delivered, for the attempt the caller won. The attempt is part of the predicate so a stamp from a send that an operator clear has since superseded matches no row — see the query.

<a name="SchedulerStore.MarkStagingDeleted"></a>
### func \(\*SchedulerStore\) [MarkStagingDeleted](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L609>)

```go
func (s *SchedulerStore) MarkStagingDeleted(ctx context.Context, pvcName, reason string) error
```

MarkStagingDeleted records that a staging volume's PVC was deleted and why \(run\_succeeded | ttl\_expired | orphaned\).

<a name="SchedulerStore.MarkTaskAgentLost"></a>
### func \(\*SchedulerStore\) [MarkTaskAgentLost](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L736>)

```go
func (s *SchedulerStore) MarkTaskAgentLost(ctx context.Context, taskInstanceID string) (bool, error)
```

MarkTaskAgentLost transitions one TI to \`failed\` with the agent\_lost reason. The WHERE state='running' guard makes this idempotent and prevents a late terminal report being overwritten — if the row already moved, we touch zero rows and return nil.

<a name="SchedulerStore.MarkTaskDispatchFailed"></a>
### func \(\*SchedulerStore\) [MarkTaskDispatchFailed](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L720>)

```go
func (s *SchedulerStore) MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
```

MarkTaskDispatchFailed transitions a TI to \`failed\` after its asynchronous dispatch failed inside the BufferedDispatcher worker \(\#127\). The SQL guard only targets scheduled/queued rows, so a TI that already moved to running or terminal between the worker accepting the request and the dispatch failing is left alone \(defense in depth — the agent's late progress report wins over the dispatcher's "I failed" claim\).

<a name="SchedulerStore.MarkTaskDispatchLost"></a>
### func \(\*SchedulerStore\) [MarkTaskDispatchLost](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L823>)

```go
func (s *SchedulerStore) MarkTaskDispatchLost(ctx context.Context, taskInstanceID string) error
```

MarkTaskDispatchLost transitions one TI to \`failed\` with the dispatch\_lost reason. The WHERE state='queued' guard makes this idempotent: a TI that has since been dispatched \(real progress landed\) is left alone.

<a name="SchedulerStore.MarkTaskPodLost"></a>
### func \(\*SchedulerStore\) [MarkTaskPodLost](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L863>)

```go
func (s *SchedulerStore) MarkTaskPodLost(ctx context.Context, taskInstanceID string) (bool, error)
```

MarkTaskPodLost transitions one TI to \`failed\` with the pod\_lost reason. The WHERE state='running' guard makes it idempotent: a TI that has since moved on \(a late terminal report landed\) is left alone.

<a name="SchedulerStore.MaterializeTasks"></a>
### func \(\*SchedulerStore\) [MaterializeTasks](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L270>)

```go
func (s *SchedulerStore) MaterializeTasks(ctx context.Context, runID string, tasks []domain.TaskSpec) error
```

MaterializeTasks creates a none\-state task instance for each task in the run, in one batched COPY rather than T INSERT round\-trips. The rows are identical to the per\-task loop this replaced: try\_number pinned to 1, state none, and max\_tries derived from the task's retries \(default 1\). An empty task set is a no\-op \(a DAG with no tasks materializes nothing\).

<a name="SchedulerStore.PoolBudgets"></a>
### func \(\*SchedulerStore\) [PoolBudgets](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L323>)

```go
func (s *SchedulerStore) PoolBudgets(ctx context.Context) (map[string]int, error)
```

PoolBudgets returns every named pool's slot cap keyed by scheduler.PoolKey\(tenantID, name\) — the cross\-DAG admission budget the pool gate enforces \(ADR 0053 Stage 3\). The scheduler calls it once per tick, and only on the Pro path; Lite never loads pool budgets.

<a name="SchedulerStore.ReapRun"></a>
### func \(\*SchedulerStore\) [ReapRun](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L651>)

```go
func (s *SchedulerStore) ReapRun(ctx context.Context, runID string) error
```

ReapRun fails an orphaned dag run, then any of its still\-active task instances, inside a single transaction. The run UPDATE comes first and is guarded by \`state = 'running'\`: if zero rows are touched, the run was no longer running \(a competing finalizer beat us\) and we abort with a clean rollback — the TI table is never touched. This guarantees we cannot leave a run as \`success\`/\`failed\` while flipping its TIs to \`failed \(orphaned\)\`. Idempotent: a second call on an already\-failed run no\-ops.

<a name="SchedulerStore.RecordDispatchBackpressure"></a>
### func \(\*SchedulerStore\) [RecordDispatchBackpressure](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L446>)

```go
func (s *SchedulerStore) RecordDispatchBackpressure(ctx context.Context, runID, taskID string, nextAt time.Time) error
```

RecordDispatchBackpressure backs off a scheduled task after a retriable\-forever cluster\-backpressure dispatch failure \(quota 403 / APF 429\), setting nextAt WITHOUT incrementing dispatch\_attempts so it never accumulates toward the dispatch\_failed cap \(ADR 0053\).

<a name="SchedulerStore.RecordDispatchFailure"></a>
### func \(\*SchedulerStore\) [RecordDispatchFailure](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L430>)

```go
func (s *SchedulerStore) RecordDispatchFailure(ctx context.Context, runID, taskID string, nextAt time.Time) error
```

RecordDispatchFailure increments a scheduled task's dispatch\-failure counter and backs off its next attempt to nextAt \(ADR 0031 Amendment A\).

<a name="SchedulerStore.RecordStagingVolume"></a>
### func \(\*SchedulerStore\) [RecordStagingVolume](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L594>)

```go
func (s *SchedulerStore) RecordStagingVolume(ctx context.Context, tenantID, dagID, runID, pvcName, size string) error
```

RecordStagingVolume records a per\-run staging volume as active, keyed by PVC name \(idempotent — called per task as the PVC is ensured\). ADR 0022.

<a name="SchedulerStore.RedispatchReschedule"></a>
### func \(\*SchedulerStore\) [RedispatchReschedule](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L474>)

```go
func (s *SchedulerStore) RedispatchReschedule(ctx context.Context, runID, taskID string) error
```

RedispatchReschedule returns a task parked in up\_for\_reschedule to 'none' for re\-dispatch, preserving try\_number \(reschedule is not a retry; \#380\).

<a name="SchedulerStore.ResetForInfraReplace"></a>
### func \(\*SchedulerStore\) [ResetForInfraReplace](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L413>)

```go
func (s *SchedulerStore) ResetForInfraReplace(ctx context.Context, runID, taskID string) (bool, error)
```

ResetForInfraReplace returns a task instance failed by a reaper as infra \(last\_failure\_kind='infra'\) to 'none' so the scheduler re\-runs it, bumping infra\_attempts instead of try\_number — an infrastructure fault must not consume the user's retry budget \(ADR 0051 Phase 1\). It uses the failed\+infra\-guarded query so a late terminal report or a non\-infra failure at state='failed' cannot be re\-placed off\-budget. The bool reports whether the guarded update fired \(exactly one row\): a false means the TI was no longer a failed\-infra candidate, so the caller must not record a re\-placement it did not perform.

<a name="SchedulerStore.ResetForRetry"></a>
### func \(\*SchedulerStore\) [ResetForRetry](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L389>)

```go
func (s *SchedulerStore) ResetForRetry(ctx context.Context, runID, taskID string) (bool, error)
```

ResetForRetry returns a task instance to 'none', clears its timestamps, and increments its try number so the scheduler re\-evaluates and re\-runs it. It uses the up\_for\_retry\-guarded query so a stale retry decision cannot reset a TI that has since been re\-dispatched \(audit follow\-up; see the query doc\). The bool reports whether the guarded update actually fired \(exactly one row\): a false means the TI was no longer up\_for\_retry and nothing was reset, so the caller must not record a retry it did not perform.

<a name="SchedulerStore.ScheduledDAGs"></a>
### func \(\*SchedulerStore\) [ScheduledDAGs](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L544>)

```go
func (s *SchedulerStore) ScheduledDAGs(ctx context.Context) ([]scheduler.ScheduledDAG, error)
```

ScheduledDAGs returns active, unpaused, cron\-scheduled DAGs with the logical date of their most recent run.

<a name="SchedulerStore.SetRunState"></a>
### func \(\*SchedulerStore\) [SetRunState](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L486>)

```go
func (s *SchedulerStore) SetRunState(ctx context.Context, runID string, state domain.DagRunState) error
```

SetRunState updates a run's state.

<a name="SchedulerStore.SetTaskNote"></a>
### func \(\*SchedulerStore\) [SetTaskNote](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L370>)

```go
func (s *SchedulerStore) SetTaskNote(ctx context.Context, runID, taskID, note string) error
```

SetTaskNote attaches operational context to a task instance, shown in the UI.

<a name="SchedulerStore.SetWarmExecution"></a>
### func \(\*SchedulerStore\) [SetWarmExecution](<https://github.com/dexadata/leoflow/blob/main/internal/storage/scheduler_store.go#L176>)

```go
func (s *SchedulerStore) SetWarmExecution(exec config.ExecutionSection)
```

SetWarmExecution records the operator's warm\-pool config so ActiveWarmTargets can resolve each active dag\_version's effective warm target. main.go calls it only when warm pools are enabled; left unset, warm pools read as off \(every target 0\).

<a name="XComIndex"></a>
## type [XComIndex](<https://github.com/dexadata/leoflow/blob/main/internal/storage/xcom_index.go#L17-L19>)

XComIndex is the Postgres\-backed XCom metadata index. It implements xcom.Index, recording each pushed value so the API can find and list it.

```go
type XComIndex struct {
    // contains filtered or unexported fields
}
```

<a name="NewXComIndex"></a>
### func [NewXComIndex](<https://github.com/dexadata/leoflow/blob/main/internal/storage/xcom_index.go#L22>)

```go
func NewXComIndex(pg *Postgres) *XComIndex
```

NewXComIndex builds an XComIndex over the given Postgres connection.

<a name="XComIndex.PurgeExpired"></a>
### func \(\*XComIndex\) [PurgeExpired](<https://github.com/dexadata/leoflow/blob/main/internal/storage/xcom_index.go#L50>)

```go
func (x *XComIndex) PurgeExpired(ctx context.Context) error
```

PurgeExpired deletes xcom\_index rows past their expiry. Redis expires the values natively; this reclaims the metadata rows.

<a name="XComIndex.RecordXCom"></a>
### func \(\*XComIndex\) [RecordXCom](<https://github.com/dexadata/leoflow/blob/main/internal/storage/xcom_index.go#L27>)

```go
func (x *XComIndex) RecordXCom(ctx context.Context, e xcom.IndexEntry) error
```

RecordXCom upserts the metadata for a pushed XCom value.

<a name="XComReader"></a>
## type [XComReader](<https://github.com/dexadata/leoflow/blob/main/internal/storage/xcom_index.go#L56-L59>)

XComReader reads XCom values for the API: it resolves the Redis key from the Postgres index by name and fetches the value from the backend.

```go
type XComReader struct {
    // contains filtered or unexported fields
}
```

<a name="NewXComReader"></a>
### func [NewXComReader](<https://github.com/dexadata/leoflow/blob/main/internal/storage/xcom_index.go#L63>)

```go
func NewXComReader(pg *Postgres, backend xcom.Backend) *XComReader
```

NewXComReader builds an XComReader over the given Postgres connection and XCom backend.

<a name="XComReader.GetXCom"></a>
### func \(\*XComReader\) [GetXCom](<https://github.com/dexadata/leoflow/blob/main/internal/storage/xcom_index.go#L69>)

```go
func (r *XComReader) GetXCom(ctx context.Context, tenant, dagID, runID, taskID, key string) (xcom.Entry, error)
```

GetXCom returns the XCom entry for the named value, or domain.ErrNotFound when it is absent or expired \(in the index or in Redis\).

<a name="XComReader.ListXComEntries"></a>
### func \(\*XComReader\) [ListXComEntries](<https://github.com/dexadata/leoflow/blob/main/internal/storage/xcom_index.go#L91>)

```go
func (r *XComReader) ListXComEntries(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.XComEntryMeta, error)
```

ListXComEntries returns the metadata of every non\-expired XCom pushed by a task instance \(keys and timestamps, no values\), for the XCom list view.

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
