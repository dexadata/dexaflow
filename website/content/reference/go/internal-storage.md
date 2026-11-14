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

- [Constants](<#constants>)
- [Variables](<#variables>)
- [func AttachRedisObservability\(ctx context.Context, r \*Redis, m RedisMetrics, interval time.Duration\) func\(\)](<#AttachRedisObservability>)
- [func HoldsKeyMigrationLock\(ctx context.Context, q RowQueryer\) \(bool, error\)](<#HoldsKeyMigrationLock>)
- [func KeyMigrationLockHeld\(ctx context.Context, q RowQueryer\) \(bool, error\)](<#KeyMigrationLockHeld>)
- [func NewLeaderPool\(ctx context.Context, cfg config.DatabaseSection\) \(\*pgxpool.Pool, error\)](<#NewLeaderPool>)
- [func TryKeyMigrationLockExclusive\(ctx context.Context, q RowQueryer\) \(bool, error\)](<#TryKeyMigrationLockExclusive>)
- [func TryKeyMigrationLockShared\(ctx context.Context, q RowQueryer\) \(bool, error\)](<#TryKeyMigrationLockShared>)
- [func WatchRedisMemory\(ctx context.Context, r \*Redis, m RedisMemoryMetrics, logger \*slog.Logger, interval time.Duration\) func\(\)](<#WatchRedisMemory>)
- [type EncryptedColumn](<#EncryptedColumn>)
  - [func EncryptedColumns\(\) \[\]EncryptedColumn](<#EncryptedColumns>)
  - [func \(c EncryptedColumn\) String\(\) string](<#EncryptedColumn.String>)
- [type ExecutionStore](<#ExecutionStore>)
  - [func NewExecutionStore\(pg \*Postgres\) \*ExecutionStore](<#NewExecutionStore>)
  - [func \(s \*ExecutionStore\) BindWarmAttempt\(ctx context.Context, runID, taskID string, tryNumber, attemptEpoch int, workerPod string\) error](<#ExecutionStore.BindWarmAttempt>)
  - [func \(s \*ExecutionStore\) FailTask\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string\) error](<#ExecutionStore.FailTask>)
  - [func \(s \*ExecutionStore\) FailTaskInfra\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string\) error](<#ExecutionStore.FailTaskInfra>)
  - [func \(s \*ExecutionStore\) IsTaskInstanceLive\(ctx context.Context, id auth.AgentIdentity\) \(bool, error\)](<#ExecutionStore.IsTaskInstanceLive>)
  - [func \(s \*ExecutionStore\) OnPool\(pg \*Postgres\) \*ExecutionStore](<#ExecutionStore.OnPool>)
  - [func \(s \*ExecutionStore\) RecordHeartbeat\(ctx context.Context, id auth.AgentIdentity\) error](<#ExecutionStore.RecordHeartbeat>)
  - [func \(s \*ExecutionStore\) ReportState\(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string\) error](<#ExecutionStore.ReportState>)
  - [func \(s \*ExecutionStore\) RequeueForRedispatch\(ctx context.Context, runID, taskID string, tryNumber, attemptEpoch int\) error](<#ExecutionStore.RequeueForRedispatch>)
  - [func \(s \*ExecutionStore\) Reschedule\(ctx context.Context, id auth.AgentIdentity, at time.Time\) error](<#ExecutionStore.Reschedule>)
  - [func \(s \*ExecutionStore\) RescheduleTask\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, at time.Time\) error](<#ExecutionStore.RescheduleTask>)
  - [func \(s \*ExecutionStore\) ResolveTask\(ctx context.Context, runID, taskID string\) \(dispatch.Resolved, error\)](<#ExecutionStore.ResolveTask>)
  - [func \(s \*ExecutionStore\) SetQueueWaitObserver\(fn func\(tenant string, seconds float64\)\)](<#ExecutionStore.SetQueueWaitObserver>)
  - [func \(s \*ExecutionStore\) SettledRuns\(ctx context.Context, refs \[\]executor.RunRef\) \(map\[executor.RunRef\]bool, error\)](<#ExecutionStore.SettledRuns>)
  - [func \(s \*ExecutionStore\) SucceedTask\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) error](<#ExecutionStore.SucceedTask>)
  - [func \(s \*ExecutionStore\) SucceedTaskIfActive\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(bool, error\)](<#ExecutionStore.SucceedTaskIfActive>)
  - [func \(s \*ExecutionStore\) SucceedTaskOverInfraMark\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(executor.InfraOverride, bool, error\)](<#ExecutionStore.SucceedTaskOverInfraMark>)
  - [func \(s \*ExecutionStore\) TaskSpec\(ctx context.Context, id auth.AgentIdentity\) \(agentrpc.TaskSpec, error\)](<#ExecutionStore.TaskSpec>)
- [type KeyMigrationHooks](<#KeyMigrationHooks>)
- [type KeyMigrationResult](<#KeyMigrationResult>)
  - [func MigrateSecretValues\(ctx context.Context, db TxBeginner, enc secrets.Cipher, predecessors \[\]secrets.Cipher, hooks KeyMigrationHooks\) \(KeyMigrationResult, error\)](<#MigrateSecretValues>)
  - [func \(r KeyMigrationResult\) Clean\(\) bool](<#KeyMigrationResult.Clean>)
- [type LogReader](<#LogReader>)
  - [func NewLogReader\(pg \*Postgres, sink logs.Sink, tailer logs.Tailer\) \*LogReader](<#NewLogReader>)
  - [func \(r \*LogReader\) ReadLogs\(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int\) \(io.ReadCloser, error\)](<#LogReader.ReadLogs>)
  - [func \(r \*LogReader\) Tail\(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int\) \(lines \<\-chan string, cancel func\(\), err error\)](<#LogReader.Tail>)
- [type Option](<#Option>)
  - [func WithCipher\(c secrets.Cipher\) Option](<#WithCipher>)
  - [func WithDagsArePausedAtCreation\(paused bool\) Option](<#WithDagsArePausedAtCreation>)
  - [func WithExternalSecretCoverage\(c externalSecretCoverage\) Option](<#WithExternalSecretCoverage>)
  - [func WithVariableEncryption\(m VariableEncryption\) Option](<#WithVariableEncryption>)
  - [func WithXComBackend\(b XComValueDeleter\) Option](<#WithXComBackend>)
- [type PoolCollector](<#PoolCollector>)
  - [func NewPoolCollector\(pg \*Postgres\) \*PoolCollector](<#NewPoolCollector>)
  - [func \(c \*PoolCollector\) Collect\(ch chan\<\- prometheus.Metric\)](<#PoolCollector.Collect>)
  - [func \(c \*PoolCollector\) Describe\(ch chan\<\- \*prometheus.Desc\)](<#PoolCollector.Describe>)
- [type Postgres](<#Postgres>)
  - [func NewPostgres\(ctx context.Context, cfg config.DatabaseSection\) \(\*Postgres, error\)](<#NewPostgres>)
  - [func \(p \*Postgres\) CheckRowSecurity\(ctx context.Context\) error](<#Postgres.CheckRowSecurity>)
  - [func \(p \*Postgres\) Close\(\)](<#Postgres.Close>)
  - [func \(p \*Postgres\) ForAgents\(ctx context.Context, cfg config.DatabaseSection\) \(\*Postgres, func\(\), error\)](<#Postgres.ForAgents>)
  - [func \(p \*Postgres\) ForScheduler\(ctx context.Context, cfg config.DatabaseSection\) \(\*Postgres, func\(\), error\)](<#Postgres.ForScheduler>)
  - [func \(p \*Postgres\) Ping\(ctx context.Context\) error](<#Postgres.Ping>)
  - [func \(p \*Postgres\) SchemaReady\(ctx context.Context\) error](<#Postgres.SchemaReady>)
  - [func \(p \*Postgres\) SchemaVersion\(ctx context.Context\) \(version uint, dirty, exists bool, err error\)](<#Postgres.SchemaVersion>)
  - [func \(p \*Postgres\) SchemaWaitState\(ctx context.Context\) \(SchemaWait, error\)](<#Postgres.SchemaWaitState>)
  - [func \(p \*Postgres\) SetOnReplaced\(f func\(\)\)](<#Postgres.SetOnReplaced>)
  - [func \(p \*Postgres\) SetRecoveryHint\(hint func\(class string\) string\)](<#Postgres.SetRecoveryHint>)
  - [func \(p \*Postgres\) SetSchemaObserver\(observe func\(class string\)\)](<#Postgres.SetSchemaObserver>)
- [type Redis](<#Redis>)
  - [func NewRedis\(ctx context.Context, cfg config.RedisSection\) \(\*Redis, error\)](<#NewRedis>)
  - [func \(r \*Redis\) Close\(\) error](<#Redis.Close>)
  - [func \(r \*Redis\) MemoryInfo\(ctx context.Context\) \(RedisMemoryInfo, error\)](<#Redis.MemoryInfo>)
  - [func \(r \*Redis\) Ping\(ctx context.Context\) error](<#Redis.Ping>)
- [type RedisMemoryInfo](<#RedisMemoryInfo>)
  - [func CheckRedisEviction\(ctx context.Context, r \*Redis, require bool, logger \*slog.Logger\) \(RedisMemoryInfo, error\)](<#CheckRedisEviction>)
  - [func \(i RedisMemoryInfo\) EvictionSafe\(\) bool](<#RedisMemoryInfo.EvictionSafe>)
- [type RedisMemoryMetrics](<#RedisMemoryMetrics>)
- [type RedisMetrics](<#RedisMetrics>)
- [type RedisRateCounter](<#RedisRateCounter>)
  - [func NewRedisRateCounter\(client \*redis.Client\) \*RedisRateCounter](<#NewRedisRateCounter>)
  - [func \(r \*RedisRateCounter\) Decr\(ctx context.Context, key string\) error](<#RedisRateCounter.Decr>)
  - [func \(r \*RedisRateCounter\) Get\(ctx context.Context, key string\) \(int64, error\)](<#RedisRateCounter.Get>)
  - [func \(r \*RedisRateCounter\) Incr\(ctx context.Context, key string, ttl time.Duration\) \(int64, error\)](<#RedisRateCounter.Incr>)
- [type ReencryptResult](<#ReencryptResult>)
  - [func \(r ReencryptResult\) Clean\(\) bool](<#ReencryptResult.Clean>)
- [type Repository](<#Repository>)
  - [func NewRepository\(pg \*Postgres, opts ...Option\) \*Repository](<#NewRepository>)
  - [func NewServerRepository\(pg \*Postgres, opts ...Option\) \(\*Repository, error\)](<#NewServerRepository>)
  - [func \(r \*Repository\) AddFavorite\(ctx context.Context, tenant, userID, dagID string\) error](<#Repository.AddFavorite>)
  - [func \(r \*Repository\) AdminUserState\(ctx context.Context, tenant, email string\) \(auth.AdminState, error\)](<#Repository.AdminUserState>)
  - [func \(r \*Repository\) AlertEndpoint\(ctx context.Context, tenantID, connID string\) \(endpointURL string, headers map\[string\]string, err error\)](<#Repository.AlertEndpoint>)
  - [func \(r \*Repository\) BootstrapAdmin\(ctx context.Context, tenant, email, password string\) \(bool, error\)](<#Repository.BootstrapAdmin>)
  - [func \(r \*Repository\) BootstrapAdminHash\(ctx context.Context, tenant, email, hash string\) \(bool, error\)](<#Repository.BootstrapAdminHash>)
  - [func \(r \*Repository\) ClearDagHistory\(ctx context.Context, tenant, dagID string\) error](<#Repository.ClearDagHistory>)
  - [func \(r \*Repository\) ClearImportError\(ctx context.Context, tenant, filename string\) error](<#Repository.ClearImportError>)
  - [func \(r \*Repository\) ClearTaskInstances\(ctx context.Context, tenant, dagID, runID string, taskIDs \[\]string, onlyFailed bool, opts domain.ClearOptions\) \(int, error\)](<#Repository.ClearTaskInstances>)
  - [func \(r \*Repository\) ConvergeVariableEncryption\(ctx context.Context\) \(VariableSweepResult, error\)](<#Repository.ConvergeVariableEncryption>)
  - [func \(r \*Repository\) CreateAPIToken\(ctx context.Context, tenant, userID, name, hash string, scopes \[\]string, expiresAt time.Time\) \(auth.APIToken, error\)](<#Repository.CreateAPIToken>)
  - [func \(r \*Repository\) CreateCLIDeviceAuthorization\(ctx context.Context, req auth.DeviceAuthorizationRequest\) \(auth.DeviceAuthorization, error\)](<#Repository.CreateCLIDeviceAuthorization>)
  - [func \(r \*Repository\) CreateDagRun\(ctx context.Context, tenant, dagID string, run domain.DagRun\) \(domain.DagRun, error\)](<#Repository.CreateDagRun>)
  - [func \(r \*Repository\) CreateOIDCUser\(ctx context.Context, tenant, email, provider, subject string, roles \[\]string\) \(\*auth.User, error\)](<#Repository.CreateOIDCUser>)
  - [func \(r \*Repository\) CreateUser\(ctx context.Context, tenant, email, password string, roles \[\]string\) \(domain.User, error\)](<#Repository.CreateUser>)
  - [func \(r \*Repository\) DagRunVersionLabels\(ctx context.Context, tenant, dagID string, runIDs \[\]string\) \(map\[string\]string, error\)](<#Repository.DagRunVersionLabels>)
  - [func \(r \*Repository\) DagStats\(ctx context.Context, tenant string\) \(domain.DagStats, error\)](<#Repository.DagStats>)
  - [func \(r \*Repository\) DagsArePausedAtCreation\(\) bool](<#Repository.DagsArePausedAtCreation>)
  - [func \(r \*Repository\) DecideCLIDeviceAuthorization\(ctx context.Context, id string, approve bool, user auth.User\) error](<#Repository.DecideCLIDeviceAuthorization>)
  - [func \(r \*Repository\) DeleteConnection\(ctx context.Context, tenant, connID string\) error](<#Repository.DeleteConnection>)
  - [func \(r \*Repository\) DeleteDag\(ctx context.Context, tenant, dagID string\) error](<#Repository.DeleteDag>)
  - [func \(r \*Repository\) DeleteDagRun\(ctx context.Context, tenant, dagID, runID string\) error](<#Repository.DeleteDagRun>)
  - [func \(r \*Repository\) DeleteExpiredCLIDeviceAuthorizations\(ctx context.Context\) \(int64, error\)](<#Repository.DeleteExpiredCLIDeviceAuthorizations>)
  - [func \(r \*Repository\) DeletePool\(ctx context.Context, tenant, name string\) error](<#Repository.DeletePool>)
  - [func \(r \*Repository\) DeleteVariable\(ctx context.Context, tenant, key string\) error](<#Repository.DeleteVariable>)
  - [func \(r \*Repository\) EnsureIssuerUser\(ctx context.Context, tenant, email, provider, subject string, roles \[\]string\) \(found \*auth.User, ok bool, err error\)](<#Repository.EnsureIssuerUser>)
  - [func \(r \*Repository\) EnsureTenant\(ctx context.Context, name, displayName string, defaultPoolSlots int, limits domain.TenantLimitsUpdate\) \(created bool, err error\)](<#Repository.EnsureTenant>)
  - [func \(r \*Repository\) FavoriteDagIDs\(ctx context.Context, tenant, userID string\) \(map\[string\]bool, error\)](<#Repository.FavoriteDagIDs>)
  - [func \(r \*Repository\) FindAPITokenByHash\(ctx context.Context, hash string\) \(\*auth.APIToken, error\)](<#Repository.FindAPITokenByHash>)
  - [func \(r \*Repository\) FindCLIDeviceByUserCode\(ctx context.Context, userCodeHash string\) \(auth.DeviceAuthorization, error\)](<#Repository.FindCLIDeviceByUserCode>)
  - [func \(r \*Repository\) FindUserByID\(ctx context.Context, id string\) \(found \*auth.User, ok bool, err error\)](<#Repository.FindUserByID>)
  - [func \(r \*Repository\) FindUserByLogin\(ctx context.Context, tenant, username string\) \(found \*auth.User, passwordHash string, err error\)](<#Repository.FindUserByLogin>)
  - [func \(r \*Repository\) FindUserByOIDCSubject\(ctx context.Context, provider, subject string\) \(found \*auth.User, ok bool, err error\)](<#Repository.FindUserByOIDCSubject>)
  - [func \(r \*Repository\) GetConnection\(ctx context.Context, tenant, connID string\) \(domain.Connection, error\)](<#Repository.GetConnection>)
  - [func \(r \*Repository\) GetCurrentSpec\(ctx context.Context, tenant, dagID string\) \(domain.DAGSpec, error\)](<#Repository.GetCurrentSpec>)
  - [func \(r \*Repository\) GetDag\(ctx context.Context, tenant, dagID string\) \(domain.DAG, error\)](<#Repository.GetDag>)
  - [func \(r \*Repository\) GetDagRun\(ctx context.Context, tenant, dagID, runID string\) \(domain.DagRun, error\)](<#Repository.GetDagRun>)
  - [func \(r \*Repository\) GetDagVersionSpec\(ctx context.Context, tenant, dagID string, versionNumber int\) \(domain.DagVersion, domain.DAGSpec, error\)](<#Repository.GetDagVersionSpec>)
  - [func \(r \*Repository\) GetPool\(ctx context.Context, tenant, name string\) \(domain.Pool, error\)](<#Repository.GetPool>)
  - [func \(r \*Repository\) GetVariable\(ctx context.Context, tenant, key string\) \(domain.Variable, error\)](<#Repository.GetVariable>)
  - [func \(r \*Repository\) HasCipher\(\) bool](<#Repository.HasCipher>)
  - [func \(r \*Repository\) HistoricalMetrics\(ctx context.Context, tenant string, since, until time.Time\) \(domain.HistoricalMetrics, error\)](<#Repository.HistoricalMetrics>)
  - [func \(r \*Repository\) InvalidateTenant\(name string\)](<#Repository.InvalidateTenant>)
  - [func \(r \*Repository\) LatestRunsForDags\(ctx context.Context, tenant string, dagIDs \[\]string, perDag int\) \(map\[string\]\[\]domain.DagRun, error\)](<#Repository.LatestRunsForDags>)
  - [func \(r \*Repository\) ListAPITokens\(ctx context.Context, tenant, ownerID string, limit, offset int\) \(\[\]auth.APIToken, int, error\)](<#Repository.ListAPITokens>)
  - [func \(r \*Repository\) ListAuditLogs\(ctx context.Context, tenant, dagID string, limit, offset int\) \(\[\]domain.AuditLogEntry, int, error\)](<#Repository.ListAuditLogs>)
  - [func \(r \*Repository\) ListAuditLogsAfter\(ctx context.Context, tenant, dagID string, after domain.PageCursor, limit int\) \(\[\]domain.AuditLogEntry, int, error\)](<#Repository.ListAuditLogsAfter>)
  - [func \(r \*Repository\) ListConnections\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.Connection, int, error\)](<#Repository.ListConnections>)
  - [func \(r \*Repository\) ListConnectionsFiltered\(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int\) \(conns \[\]domain.Connection, total int, err error\)](<#Repository.ListConnectionsFiltered>)
  - [func \(r \*Repository\) ListDagRuns\(ctx context.Context, tenant, dagID string, limit, offset int\) \(\[\]domain.DagRun, int, error\)](<#Repository.ListDagRuns>)
  - [func \(r \*Repository\) ListDagRunsAfter\(ctx context.Context, tenant, dagID string, states \[\]string, after domain.PageCursor, limit int\) \(\[\]domain.DagRun, int, error\)](<#Repository.ListDagRunsAfter>)
  - [func \(r \*Repository\) ListDagRunsPage\(ctx context.Context, tenant, dagID string, limit, offset int\) \(\[\]domain.DagRun, error\)](<#Repository.ListDagRunsPage>)
  - [func \(r \*Repository\) ListDagRunsWithFilter\(ctx context.Context, tenant, dagID string, filter domain.DagRunFilter\) \(runs \[\]domain.DagRun, total int, err error\)](<#Repository.ListDagRunsWithFilter>)
  - [func \(r \*Repository\) ListDagTags\(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int\) \(tags \[\]string, total int, err error\)](<#Repository.ListDagTags>)
  - [func \(r \*Repository\) ListDagVersions\(ctx context.Context, tenant, dagID string\) \(\[\]domain.DagVersion, error\)](<#Repository.ListDagVersions>)
  - [func \(r \*Repository\) ListDags\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.DAG, int, error\)](<#Repository.ListDags>)
  - [func \(r \*Repository\) ListDagsFiltered\(ctx context.Context, tenant, runState string, paused \*bool, limit, offset int\) \(\[\]domain.DAG, int, error\)](<#Repository.ListDagsFiltered>)
  - [func \(r \*Repository\) ListDagsWithFilter\(ctx context.Context, tenant string, filter domain.DagFilter\) \(\[\]domain.DAG, int, error\)](<#Repository.ListDagsWithFilter>)
  - [func \(r \*Repository\) ListImportErrors\(ctx context.Context, tenant string\) \(\[\]domain.ImportError, error\)](<#Repository.ListImportErrors>)
  - [func \(r \*Repository\) ListImportErrorsFiltered\(ctx context.Context, tenant, filename, bundleName string, limit, offset int\) \(errs \[\]domain.ImportError, total int, err error\)](<#Repository.ListImportErrorsFiltered>)
  - [func \(r \*Repository\) ListPools\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.Pool, int, error\)](<#Repository.ListPools>)
  - [func \(r \*Repository\) ListTaskInstanceAttempts\(ctx context.Context, tenant, dagID, runID, taskID string\) \(\[\]domain.TaskInstance, error\)](<#Repository.ListTaskInstanceAttempts>)
  - [func \(r \*Repository\) ListTaskInstances\(ctx context.Context, tenant, dagID, runID string, \_, \_ int\) \(\[\]domain.TaskInstance, int, error\)](<#Repository.ListTaskInstances>)
  - [func \(r \*Repository\) ListTaskInstancesWithFilter\(ctx context.Context, tenant, dagID, runID string, filter domain.TaskInstanceFilter\) \(tis \[\]domain.TaskInstance, total int, err error\)](<#Repository.ListTaskInstancesWithFilter>)
  - [func \(r \*Repository\) ListUsers\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.User, int, error\)](<#Repository.ListUsers>)
  - [func \(r \*Repository\) ListVariables\(ctx context.Context, tenant string, limit, offset int\) \(\[\]domain.Variable, int, error\)](<#Repository.ListVariables>)
  - [func \(r \*Repository\) ListVariablesFiltered\(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int\) \(vars \[\]domain.Variable, total int, err error\)](<#Repository.ListVariablesFiltered>)
  - [func \(r \*Repository\) LocalPasswordUserExists\(ctx context.Context, tenant, email string\) \(bool, error\)](<#Repository.LocalPasswordUserExists>)
  - [func \(r \*Repository\) PollCLIDeviceAuthorization\(ctx context.Context, deviceCodeHash string\) \(auth.DevicePoll, error\)](<#Repository.PollCLIDeviceAuthorization>)
  - [func \(r \*Repository\) PoolSlotUsage\(ctx context.Context, tenant string\) \(map\[string\]domain.PoolUsage, error\)](<#Repository.PoolSlotUsage>)
  - [func \(r \*Repository\) ReconcileUserRoles\(ctx context.Context, userID string, roleNames \[\]string\) error](<#Repository.ReconcileUserRoles>)
  - [func \(r \*Repository\) RecordAuthEvent\(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map\[string\]string\) error](<#Repository.RecordAuthEvent>)
  - [func \(r \*Repository\) RecordResourceAudit\(ctx context.Context, tenant, actorUserID, action, resourceType, resourceID string, metadata map\[string\]string\) error](<#Repository.RecordResourceAudit>)
  - [func \(r \*Repository\) RecordRunActionAudit\(ctx context.Context, tenant, userID, action, dagID, runID string, metadata map\[string\]string\) error](<#Repository.RecordRunActionAudit>)
  - [func \(r \*Repository\) RecordSecretLivenessDenial\(ctx context.Context, tenantID, dagID, runID, taskID string, tryNumber int, kind, mode string\) error](<#Repository.RecordSecretLivenessDenial>)
  - [func \(r \*Repository\) RecordSecretScopeWarning\(ctx context.Context, tenantID, dagID, runID, taskID, kind string, declared, total int\) error](<#Repository.RecordSecretScopeWarning>)
  - [func \(r \*Repository\) RecordTaskActionAudit\(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int\) error](<#Repository.RecordTaskActionAudit>)
  - [func \(r \*Repository\) RecordUserCreatedAudit\(ctx context.Context, tenant, actorUserID, createdUserID, email, roles string\) error](<#Repository.RecordUserCreatedAudit>)
  - [func \(r \*Repository\) ReencryptSecrets\(ctx context.Context\) \(ReencryptResult, error\)](<#Repository.ReencryptSecrets>)
  - [func \(r \*Repository\) RegisterDagVersion\(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string\) \(bool, error\)](<#Repository.RegisterDagVersion>)
  - [func \(r \*Repository\) RegisterDagVersionAs\(ctx context.Context, tenant string, reg domain.DagRegistration\) \(domain.DagRegistered, error\)](<#Repository.RegisterDagVersionAs>)
  - [func \(r \*Repository\) RegisterDagVersionBy\(ctx context.Context, tenant, actorUserID string, spec domain.DAGSpec, specHash string\) \(bool, error\)](<#Repository.RegisterDagVersionBy>)
  - [func \(r \*Repository\) RemoveFavorite\(ctx context.Context, tenant, userID, dagID string\) error](<#Repository.RemoveFavorite>)
  - [func \(r \*Repository\) ResumeTenant\(ctx context.Context, tenant string\) error](<#Repository.ResumeTenant>)
  - [func \(r \*Repository\) RevokeAPIToken\(ctx context.Context, tenant, id, ownerID string\) \(auth.APIToken, error\)](<#Repository.RevokeAPIToken>)
  - [func \(r \*Repository\) RoleExists\(ctx context.Context, tenant, role string\) \(bool, error\)](<#Repository.RoleExists>)
  - [func \(r \*Repository\) RolePermissions\(ctx context.Context, tenant, role string\) \(\[\]auth.Permission, error\)](<#Repository.RolePermissions>)
  - [func \(r \*Repository\) RunsKeepingVersion\(ctx context.Context, tenant, dagID string, runIDs \[\]string, opts domain.ClearOptions\) \(\[\]domain.ClearVersionKept, error\)](<#Repository.RunsKeepingVersion>)
  - [func \(r \*Repository\) SecretConnectionURIs\(ctx context.Context, tenantID string\) \(map\[string\]string, error\)](<#Repository.SecretConnectionURIs>)
  - [func \(r \*Repository\) SecretConnectionURIsScoped\(ctx context.Context, tenantID string, names \[\]string\) \(map\[string\]string, error\)](<#Repository.SecretConnectionURIsScoped>)
  - [func \(r \*Repository\) SecretVariables\(ctx context.Context, tenantID string\) \(map\[string\]string, error\)](<#Repository.SecretVariables>)
  - [func \(r \*Repository\) SecretVariablesScoped\(ctx context.Context, tenantID string, names \[\]string\) \(map\[string\]string, error\)](<#Repository.SecretVariablesScoped>)
  - [func \(r \*Repository\) SetConnection\(ctx context.Context, tenant string, c domain.Connection\) error](<#Repository.SetConnection>)
  - [func \(r \*Repository\) SetConnectionPatch\(ctx context.Context, tenant string, p domain.ConnectionPatch\) error](<#Repository.SetConnectionPatch>)
  - [func \(r \*Repository\) SetCurrentDagVersion\(ctx context.Context, tenant, dagID string, sel domain.DagVersionSelector, actorUserID string, admit func\(domain.DAGSpec\) error\) \(domain.CurrentVersionChange, error\)](<#Repository.SetCurrentDagVersion>)
  - [func \(r \*Repository\) SetCurrentDagVersionAs\(ctx context.Context, tenant string, req domain.DagSetCurrent\) \(domain.CurrentVersionChange, error\)](<#Repository.SetCurrentDagVersionAs>)
  - [func \(r \*Repository\) SetDagRunState\(ctx context.Context, tenant, dagID, runID, state string\) error](<#Repository.SetDagRunState>)
  - [func \(r \*Repository\) SetImportError\(ctx context.Context, tenant string, e domain.ImportError\) error](<#Repository.SetImportError>)
  - [func \(r \*Repository\) SetPaused\(ctx context.Context, tenant, dagID string, paused bool\) \(domain.DAG, error\)](<#Repository.SetPaused>)
  - [func \(r \*Repository\) SetPausedBy\(ctx context.Context, tenant, actorUserID, dagID string, paused bool\) \(domain.DAG, error\)](<#Repository.SetPausedBy>)
  - [func \(r \*Repository\) SetPool\(ctx context.Context, tenant string, p domain.Pool\) error](<#Repository.SetPool>)
  - [func \(r \*Repository\) SetTaskInstanceState\(ctx context.Context, tenant, dagID, runID, taskID, state string\) error](<#Repository.SetTaskInstanceState>)
  - [func \(r \*Repository\) SetUserActive\(ctx context.Context, tenant, actorUserID, userID string, active bool\) \(domain.User, error\)](<#Repository.SetUserActive>)
  - [func \(r \*Repository\) SetUserPassword\(ctx context.Context, tenant, email, hash string\) \(bool, error\)](<#Repository.SetUserPassword>)
  - [func \(r \*Repository\) SetVariable\(ctx context.Context, tenant string, v domain.Variable\) error](<#Repository.SetVariable>)
  - [func \(r \*Repository\) SetVariablePatch\(ctx context.Context, tenant string, p domain.VariablePatch\) error](<#Repository.SetVariablePatch>)
  - [func \(r \*Repository\) SuspendTenant\(ctx context.Context, tenant string, s domain.TenantSuspension\) error](<#Repository.SuspendTenant>)
  - [func \(r \*Repository\) TaskInstancesForRuns\(ctx context.Context, tenant, dagID string, runIDs \[\]string\) \(\[\]domain.TaskInstance, error\)](<#Repository.TaskInstancesForRuns>)
  - [func \(r \*Repository\) TenantExists\(ctx context.Context, name string\) \(bool, error\)](<#Repository.TenantExists>)
  - [func \(r \*Repository\) TenantHasDefaultPool\(ctx context.Context, tenant string\) \(bool, error\)](<#Repository.TenantHasDefaultPool>)
  - [func \(r \*Repository\) TenantLimits\(ctx context.Context, tenant string\) \(domain.TenantLimits, error\)](<#Repository.TenantLimits>)
  - [func \(r \*Repository\) TenantRolePermissions\(ctx context.Context, tenant string\) \(\[\]string, error\)](<#Repository.TenantRolePermissions>)
  - [func \(r \*Repository\) TenantSuspension\(ctx context.Context, tenant string\) \(domain.TenantSuspension, error\)](<#Repository.TenantSuspension>)
  - [func \(r \*Repository\) TenantUUID\(ctx context.Context, name string\) \(string, error\)](<#Repository.TenantUUID>)
  - [func \(r \*Repository\) TouchAPIToken\(ctx context.Context, id string, at time.Time\) \(bool, error\)](<#Repository.TouchAPIToken>)
- [type RetentionStore](<#RetentionStore>)
  - [func NewRetentionStore\(pg \*Postgres\) \*RetentionStore](<#NewRetentionStore>)
  - [func \(s \*RetentionStore\) CountEligible\(ctx context.Context, runCutoff, auditCutoff, usageCutoff \*time.Time\) \(retention.Counts, error\)](<#RetentionStore.CountEligible>)
  - [func \(s \*RetentionStore\) CountEligibleForTenant\(ctx context.Context, tenant string, runCutoff, auditCutoff \*time.Time\) \(retention.Counts, error\)](<#RetentionStore.CountEligibleForTenant>)
  - [func \(s \*RetentionStore\) DeleteAuditLog\(ctx context.Context, tenant string, cutoff time.Time, limit int\) \(int64, error\)](<#RetentionStore.DeleteAuditLog>)
  - [func \(s \*RetentionStore\) DeleteFinishedRuns\(ctx context.Context, tenant string, cutoff time.Time, maxRuns, rowLimit int\) \(retention.Counts, error\)](<#RetentionStore.DeleteFinishedRuns>)
  - [func \(s \*RetentionStore\) DeleteUsageEvents\(ctx context.Context, cutoff time.Time, limit int\) \(int64, error\)](<#RetentionStore.DeleteUsageEvents>)
  - [func \(s \*RetentionStore\) RecordRetentionPurge\(ctx context.Context, tenant string, rows int64, cutoff time.Time\) error](<#RetentionStore.RecordRetentionPurge>)
  - [func \(s \*RetentionStore\) TenantIDs\(ctx context.Context\) \(\[\]string, error\)](<#RetentionStore.TenantIDs>)
- [type RowQuerier](<#RowQuerier>)
- [type RowQueryer](<#RowQueryer>)
- [type RowSecurityStatus](<#RowSecurityStatus>)
  - [func ReadRowSecurity\(ctx context.Context, db queryer\) \(\[\]RowSecurityStatus, error\)](<#ReadRowSecurity>)
  - [func \(s RowSecurityStatus\) On\(\) bool](<#RowSecurityStatus.On>)
- [type SchedulerStore](<#SchedulerStore>)
  - [func NewSchedulerStore\(pg \*Postgres\) \*SchedulerStore](<#NewSchedulerStore>)
  - [func \(s \*SchedulerStore\) ActiveRuns\(ctx context.Context\) \(\[\]scheduler.RunState, error\)](<#SchedulerStore.ActiveRuns>)
  - [func \(s \*SchedulerStore\) ActiveWarmTargets\(ctx context.Context\) \(\[\]executor.WarmTarget, error\)](<#SchedulerStore.ActiveWarmTargets>)
  - [func \(s \*SchedulerStore\) ApplyTransition\(ctx context.Context, runID, taskID string, to domain.TaskState\) error](<#SchedulerStore.ApplyTransition>)
  - [func \(s \*SchedulerStore\) ApplyTransitions\(ctx context.Context, runID string, taskIDs \[\]string, from, to domain.TaskState\) \(\[\]string, error\)](<#SchedulerStore.ApplyTransitions>)
  - [func \(s \*SchedulerStore\) ClaimAlertAttempt\(ctx context.Context, runID string, maxAttempts int, backoff time.Duration\) \(int, error\)](<#SchedulerStore.ClaimAlertAttempt>)
  - [func \(s \*SchedulerStore\) ConfirmInfraFailure\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(bool, error\)](<#SchedulerStore.ConfirmInfraFailure>)
  - [func \(s \*SchedulerStore\) CountHeartbeatFleet\(ctx context.Context, staleBefore time.Time\) \(executor.HeartbeatFleet, error\)](<#SchedulerStore.CountHeartbeatFleet>)
  - [func \(s \*SchedulerStore\) CreateScheduledRun\(ctx context.Context, tenantID, dagID string, logical time.Time\) error](<#SchedulerStore.CreateScheduledRun>)
  - [func \(s \*SchedulerStore\) DispatchAttempts\(ctx context.Context, runID, taskID string\) \(attempts int, active bool, err error\)](<#SchedulerStore.DispatchAttempts>)
  - [func \(s \*SchedulerStore\) FailDispatchExhausted\(ctx context.Context, runID, taskID, reason string\) error](<#SchedulerStore.FailDispatchExhausted>)
  - [func \(s \*SchedulerStore\) FailDispatchRefused\(ctx context.Context, runID, taskID, reason string\) error](<#SchedulerStore.FailDispatchRefused>)
  - [func \(s \*SchedulerStore\) FailStoppedTenantTaskInstances\(ctx context.Context, tenantID string\) \(int64, error\)](<#SchedulerStore.FailStoppedTenantTaskInstances>)
  - [func \(s \*SchedulerStore\) ListActiveStagingVolumes\(ctx context.Context\) \(\[\]domain.StagingVolumeState, error\)](<#SchedulerStore.ListActiveStagingVolumes>)
  - [func \(s \*SchedulerStore\) ListAgentLostCandidates\(ctx context.Context\) \(\[\]executor.AgentLostCandidate, error\)](<#SchedulerStore.ListAgentLostCandidates>)
  - [func \(s \*SchedulerStore\) ListBusyWarmWorkerPods\(ctx context.Context\) \(map\[string\]bool, error\)](<#SchedulerStore.ListBusyWarmWorkerPods>)
  - [func \(s \*SchedulerStore\) ListProvisionalInfraFailures\(ctx context.Context\) \(\[\]executor.ProvisionalInfraFailure, error\)](<#SchedulerStore.ListProvisionalInfraFailures>)
  - [func \(s \*SchedulerStore\) ListReapCandidates\(ctx context.Context\) \(\[\]executor.ReapCandidate, error\)](<#SchedulerStore.ListReapCandidates>)
  - [func \(s \*SchedulerStore\) ListRunningTasks\(ctx context.Context, grace time.Duration, after executor.ReapCursor\) \(\[\]executor.PodLostCandidate, error\)](<#SchedulerStore.ListRunningTasks>)
  - [func \(s \*SchedulerStore\) ListStaleQueuedCandidates\(ctx context.Context, threshold time.Duration, after executor.ReapCursor\) \(\[\]executor.StaleQueuedCandidate, error\)](<#SchedulerStore.ListStaleQueuedCandidates>)
  - [func \(s \*SchedulerStore\) ListWarmBoundRunningTIs\(ctx context.Context\) \(\[\]executor.WarmBoundTI, error\)](<#SchedulerStore.ListWarmBoundRunningTIs>)
  - [func \(s \*SchedulerStore\) MarkQueued\(ctx context.Context, runID, taskID string, expectNextDispatchAt \*time.Time\) \(bool, error\)](<#SchedulerStore.MarkQueued>)
  - [func \(s \*SchedulerStore\) MarkRunAlertDelivered\(ctx context.Context, runID string, attempt int\) error](<#SchedulerStore.MarkRunAlertDelivered>)
  - [func \(s \*SchedulerStore\) MarkStagingDeleted\(ctx context.Context, pvcName, reason string\) error](<#SchedulerStore.MarkStagingDeleted>)
  - [func \(s \*SchedulerStore\) MarkTaskAgentLost\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(bool, error\)](<#SchedulerStore.MarkTaskAgentLost>)
  - [func \(s \*SchedulerStore\) MarkTaskCredentialCeiling\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(bool, error\)](<#SchedulerStore.MarkTaskCredentialCeiling>)
  - [func \(s \*SchedulerStore\) MarkTaskDispatchFailed\(ctx context.Context, runID, taskID, reason string\) error](<#SchedulerStore.MarkTaskDispatchFailed>)
  - [func \(s \*SchedulerStore\) MarkTaskDispatchLost\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(bool, error\)](<#SchedulerStore.MarkTaskDispatchLost>)
  - [func \(s \*SchedulerStore\) MarkTaskPodLost\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(bool, error\)](<#SchedulerStore.MarkTaskPodLost>)
  - [func \(s \*SchedulerStore\) MarkTaskPodLostOOMKilled\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(bool, error\)](<#SchedulerStore.MarkTaskPodLostOOMKilled>)
  - [func \(s \*SchedulerStore\) MarkTaskStartExpired\(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int\) \(bool, error\)](<#SchedulerStore.MarkTaskStartExpired>)
  - [func \(s \*SchedulerStore\) MaterializeTasks\(ctx context.Context, runID string, tasks \[\]domain.TaskSpec\) error](<#SchedulerStore.MaterializeTasks>)
  - [func \(s \*SchedulerStore\) PoolBudgets\(ctx context.Context\) \(map\[string\]int, error\)](<#SchedulerStore.PoolBudgets>)
  - [func \(s \*SchedulerStore\) ReapRun\(ctx context.Context, runID string, quietBefore time.Time\) \(bool, error\)](<#SchedulerStore.ReapRun>)
  - [func \(s \*SchedulerStore\) RecordDispatchBackpressure\(ctx context.Context, runID, taskID string, nextAt time.Time\) error](<#SchedulerStore.RecordDispatchBackpressure>)
  - [func \(s \*SchedulerStore\) RecordDispatchFailure\(ctx context.Context, runID, taskID string, nextAt time.Time\) error](<#SchedulerStore.RecordDispatchFailure>)
  - [func \(s \*SchedulerStore\) RecordDispatchUnavailable\(ctx context.Context, runID, taskID string, now time.Time, minDelay, maxDelay time.Duration\) \(since, nextAt time.Time, applied bool, err error\)](<#SchedulerStore.RecordDispatchUnavailable>)
  - [func \(s \*SchedulerStore\) RecordStagingVolume\(ctx context.Context, tenantID, dagID, runID, pvcName, size string\) error](<#SchedulerStore.RecordStagingVolume>)
  - [func \(s \*SchedulerStore\) RedispatchReschedule\(ctx context.Context, runID, taskID string\) error](<#SchedulerStore.RedispatchReschedule>)
  - [func \(s \*SchedulerStore\) RequeueDispatch\(ctx context.Context, runID, taskID string, countAttempt, clearUnavailable bool, nextAt time.Time\) \(bool, error\)](<#SchedulerStore.RequeueDispatch>)
  - [func \(s \*SchedulerStore\) ResetForInfraReplace\(ctx context.Context, runID, taskID string\) \(bool, error\)](<#SchedulerStore.ResetForInfraReplace>)
  - [func \(s \*SchedulerStore\) ResetForRetry\(ctx context.Context, runID, taskID string\) \(bool, error\)](<#SchedulerStore.ResetForRetry>)
  - [func \(s \*SchedulerStore\) ScheduledDAGs\(ctx context.Context, now time.Time\) \(\[\]scheduler.ScheduledDAG, error\)](<#SchedulerStore.ScheduledDAGs>)
  - [func \(s \*SchedulerStore\) SetNextRunAt\(ctx context.Context, tenantID, dagID string, epoch int64, at time.Time\) error](<#SchedulerStore.SetNextRunAt>)
  - [func \(s \*SchedulerStore\) SetProvisionalInfraMarks\(on bool\)](<#SchedulerStore.SetProvisionalInfraMarks>)
  - [func \(s \*SchedulerStore\) SetRunState\(ctx context.Context, runID string, from, to domain.DagRunState\) \(bool, error\)](<#SchedulerStore.SetRunState>)
  - [func \(s \*SchedulerStore\) SetTaskNote\(ctx context.Context, runID, taskID, note string\) error](<#SchedulerStore.SetTaskNote>)
  - [func \(s \*SchedulerStore\) SetWarmExecution\(exec config.ExecutionSection, policy \*domain.ExecutionPolicy, placement domain.WarmPolicy\)](<#SchedulerStore.SetWarmExecution>)
  - [func \(s \*SchedulerStore\) StartQueuedRun\(ctx context.Context, runID, tenantID string\) \(bool, error\)](<#SchedulerStore.StartQueuedRun>)
- [type SchemaWait](<#SchemaWait>)
  - [func \(w SchemaWait\) Ready\(\) bool](<#SchemaWait.Ready>)
- [type SecretValue](<#SecretValue>)
  - [func ReadSecretValues\(ctx context.Context, q RowQuerier\) \(\[\]SecretValue, error\)](<#ReadSecretValues>)
  - [func SecretValuesNotUnder\(ctx context.Context, q RowQuerier, c secrets.Cipher\) \(\[\]SecretValue, error\)](<#SecretValuesNotUnder>)
- [type TaskWaitStore](<#TaskWaitStore>)
  - [func NewTaskWaitStore\(pg \*Postgres\) \*TaskWaitStore](<#NewTaskWaitStore>)
  - [func \(s \*TaskWaitStore\) OldestWaiting\(ctx context.Context\) \(\[\]WaitingAge, error\)](<#TaskWaitStore.OldestWaiting>)
- [type TxBeginner](<#TxBeginner>)
- [type UnreadableSecretsError](<#UnreadableSecretsError>)
  - [func \(e \*UnreadableSecretsError\) Error\(\) string](<#UnreadableSecretsError.Error>)
- [type UsageStore](<#UsageStore>)
  - [func NewUsageStore\(pg \*Postgres\) \*UsageStore](<#NewUsageStore>)
  - [func \(s \*UsageStore\) EventsLag\(ctx context.Context\) \(time.Duration, error\)](<#UsageStore.EventsLag>)
  - [func \(s \*UsageStore\) Horizon\(ctx context.Context\) \(domain.UsageCursor, error\)](<#UsageStore.Horizon>)
  - [func \(s \*UsageStore\) ReadEvents\(ctx context.Context, after domain.UsageCursor, limit int\) \(domain.UsagePage, error\)](<#UsageStore.ReadEvents>)
  - [func \(s \*UsageStore\) SetEventsEnabled\(ctx context.Context, enabled bool\) \(bool, error\)](<#UsageStore.SetEventsEnabled>)
- [type VariableEncryption](<#VariableEncryption>)
  - [func ParseVariableEncryption\(s string\) \(VariableEncryption, error\)](<#ParseVariableEncryption>)
- [type VariableSweepResult](<#VariableSweepResult>)
  - [func \(r VariableSweepResult\) Clean\(\) bool](<#VariableSweepResult.Clean>)
- [type WaitingAge](<#WaitingAge>)
- [type XComIndex](<#XComIndex>)
  - [func NewXComIndex\(pg \*Postgres\) \*XComIndex](<#NewXComIndex>)
  - [func \(x \*XComIndex\) PurgeExpired\(ctx context.Context\) error](<#XComIndex.PurgeExpired>)
  - [func \(x \*XComIndex\) RecordXCom\(ctx context.Context, e xcom.IndexEntry\) error](<#XComIndex.RecordXCom>)
- [type XComReader](<#XComReader>)
  - [func NewXComReader\(pg \*Postgres, backend xcom.Backend\) \*XComReader](<#NewXComReader>)
  - [func \(r \*XComReader\) GetXCom\(ctx context.Context, tenant, dagID, runID, taskID, key string\) \(xcom.Entry, error\)](<#XComReader.GetXCom>)
  - [func \(r \*XComReader\) ListXComEntries\(ctx context.Context, tenant, dagID, runID, taskID string\) \(\[\]domain.XComEntryMeta, error\)](<#XComReader.ListXComEntries>)
- [type XComValueDeleter](<#XComValueDeleter>)


## Constants

<a name="SchemaReasonAbsent"></a>Schema reasons are the fixed classes of a schema this binary cannot serve yet \(ADR 0090 decision 2\), and the reason label of dexaflow\_schema\_ready \(decision 6\). They carry no version, so a migration that moves the version does not start a new series and restart an alert's wait; the versions are in SchemaWait.Reason.

```go
const (
    SchemaReasonAbsent     = "schema absent"
    SchemaReasonBehind     = "schema behind"
    SchemaReasonDirty      = "schema dirty"
    SchemaReasonUnreadable = "schema unreadable"
    // SchemaReasonReplaced is a database that is not the one this release
    // was installed on (decision 3): it carries another installation id,
    // none, or no schema. It wins over every other reason, since migrating
    // does not fix it.
    SchemaReasonReplaced = "database replaced"
)
```

<a name="KeyMigrationLockID"></a>KeyMigrationLockID is the Postgres advisory\-lock id that excludes a Lite server from a key migration \("LiteKey" in hex\), distinct from the scheduler's leadership lock \(ADR 0009, ADR 0065 section 3\).

A Lite server, and the \`dexaflow lite\` process supervising it, hold it SHARED on their own sessions for their whole lifetime. \`dexaflow lite migrate\-key\` takes it EXCLUSIVE for the whole migration. So a migration cannot start while a server runs against the datastore, and a server cannot start while a migration runs. Advisory locks are scoped to one database of one cluster: every party takes it in the Lite database.

```go
const KeyMigrationLockID int64 = 0x4C6974654B6579
```

<a name="MaxLiveAPITokensPerUser"></a>MaxLiveAPITokensPerUser is how many unrevoked, unexpired API tokens one user may hold \(ADR 0074\). It bounds the table and what a leaked session can mint.

```go
const MaxLiveAPITokensPerUser = 50
```

<a name="RedisSafeEvictionPolicy"></a>RedisSafeEvictionPolicy is the only maxmemory\-policy under which Redis never deletes a key to free memory. XCom values carry a TTL, so a volatile\-\* policy evicts exactly them first, and an allkeys\-\* policy evicts them too \(\#2354\).

```go
const RedisSafeEvictionPolicy = "noeviction"
```

<a name="VariableCiphertextPrefix"></a>VariableCiphertextPrefix tags a stored variable value as sealed with the secret key \(\#507\). The value column holds either a legacy plaintext value or this prefix followed by the cipher's output, so the stored form describes itself: no schema change, rows written by an older server stay readable, and a value an older server writes over a sealed one is plaintext again, which is exactly what that server meant.

```go
const VariableCiphertextPrefix = "dexaflow:enc:v1:"
```

## Variables

<a name="ErrNoXComBackend"></a>Errors NewServerRepository returns when a dependency the server must decide was not given.

```go
var (
    ErrNoXComBackend      = errors.New("storage: server repository needs WithXComBackend")
    ErrNoPausedAtCreation = errors.New("storage: server repository needs WithDagsArePausedAtCreation")
)
```

<a name="ErrKeyMigrationGuard"></a>ErrKeyMigrationGuard reports that an UPDATE of the migration matched no row, which the table lock makes impossible, so an assumption is broken.

```go
var ErrKeyMigrationGuard = errors.New("a row changed under the key migration's table lock")
```

<a name="ErrNotDispatchable"></a>ErrNotDispatchable is returned by ResolveTask when the task's row is no longer in a pre\-dispatch state \(none, scheduled or queued\): it is running or settled, so dispatching it again would start a second execution.

```go
var ErrNotDispatchable = errors.New("task instance is not in a dispatchable state")
```

<a name="ErrRedisEvictionUnsafe"></a>ErrRedisEvictionUnsafe is returned by CheckRedisEviction when redis.require\_noeviction is on and the server may evict keys, or its policy cannot be read.

```go
var ErrRedisEvictionUnsafe = errors.New("redis may evict keys")
```

<a name="ErrRowSecurityOff"></a>ErrRowSecurityOff is returned by CheckRowSecurity when a tenant table does not have row level security on and forced.

```go
var ErrRowSecurityOff = errors.New("row level security is not enforced")
```

<a name="AttachRedisObservability"></a>
## func [AttachRedisObservability](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_observability.go#L185>)

```go
func AttachRedisObservability(ctx context.Context, r *Redis, m RedisMetrics, interval time.Duration) func()
```

AttachRedisObservability registers the metrics hook and starts a goroutine that scrapes the pool stats every interval. The returned function cancels the scraper goroutine; the caller defers it \(typically the datastore cleanup chain\). Lite \(Redis nil\) is a no\-op.

The scraper is a closure so the "last seen cumulative timeouts" counter \(used to compute per\-scrape DELTAS for the Prometheus counter, since go\-redis exposes Timeouts as a cumulative value\) is goroutine\-local — no shared mutable state.

<a name="HoldsKeyMigrationLock"></a>
## func [HoldsKeyMigrationLock](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keylock.go#L53>)

```go
func HoldsKeyMigrationLock(ctx context.Context, q RowQueryer) (bool, error)
```

HoldsKeyMigrationLock reports whether the caller's own session still holds the key\-migration lock, in either mode. The lock is session\-scoped: when the connection that took it dropped and was replaced, the new session does not hold it and this returns false; a query error means the session is gone. Either way the holder has lost the lock.

<a name="KeyMigrationLockHeld"></a>
## func [KeyMigrationLockHeld](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keylock.go#L77>)

```go
func KeyMigrationLockHeld(ctx context.Context, q RowQueryer) (bool, error)
```

KeyMigrationLockHeld reports whether any session holds the key\-migration lock, in either mode, without taking it. A Lite server holds it shared for its whole life, so a command that only needs to know whether a server is running against this datastore \(\`dexaflow lite reset\-password\`, \#413\) asks here instead of trying the lock, which for a moment could make a server that is starting refuse to boot.

<a name="NewLeaderPool"></a>
## func [NewLeaderPool](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/postgres.go#L372>)

```go
func NewLeaderPool(ctx context.Context, cfg config.DatabaseSection) (*pgxpool.Pool, error)
```

NewLeaderPool opens a dedicated single\-connection pool for the scheduler advisory lock, so the session holding the lock is stable \(ADR 0009\).

"Stable" has to include not being recycled on a timer, and it did not. pg\_advisory\_lock is SESSION\-scoped: the lock lives and dies with the connection. pgxpool.ParseConfig applies a default MaxConnLifetime of one hour when the DSN carries no pool\_max\_conn\_lifetime, and leoflow's DSNs do not, so this pool's connection was replaced every hour and the leadership went with it. HoldsLock then correctly reported the lock gone and watchLeadership correctly stepped down: every layer did its job and the scheduler still gave up leadership once an hour for no reason at all.

It is not theoretical. A 15.7\-hour soak recorded 15 step\-downs, one per hour, in a single\-process deployment where nothing was contending for the lock \(\#1199\). On one replica the cost is a pause; on several it moves leadership, and everything the leader was in the middle of, on a timer nobody chose.

Recycling buys this pool nothing anyway. watchLeadership queries every 5s, so a connection that really dies is caught within a tick and the step\-down that follows is a true one.

<a name="TryKeyMigrationLockExclusive"></a>
## func [TryKeyMigrationLockExclusive](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keylock.go#L40>)

```go
func TryKeyMigrationLockExclusive(ctx context.Context, q RowQueryer) (bool, error)
```

TryKeyMigrationLockExclusive takes the key\-migration lock exclusively on the caller's session without waiting. It reports false while any server, or another migration, holds it.

<a name="TryKeyMigrationLockShared"></a>
## func [TryKeyMigrationLockShared](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keylock.go#L29>)

```go
func TryKeyMigrationLockShared(ctx context.Context, q RowQueryer) (bool, error)
```

TryKeyMigrationLockShared takes the key\-migration lock in shared mode on the caller's session without waiting. It reports false while a migration holds it.

<a name="WatchRedisMemory"></a>
## func [WatchRedisMemory](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_memory.go#L126>)

```go
func WatchRedisMemory(ctx context.Context, r *Redis, m RedisMemoryMetrics, logger *slog.Logger, interval time.Duration) func()
```

WatchRedisMemory reads INFO every interval, publishes the eviction policy, used memory and evicted keys to m, and logs a warning when the policy turns unsafe after the first read \(the boot check, CheckRedisEviction, already reported the policy it found\). The returned function stops it.

<a name="EncryptedColumn"></a>
## type [EncryptedColumn](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L17-L31>)

EncryptedColumn names one column that holds ciphertext sealed with the secret key \(ADR 0019\), with the columns that identify its row.

```go
type EncryptedColumn struct {
    // Table is the table holding the column.
    Table string
    // Column is the ciphertext column.
    Column string
    // IDColumn is the row's primary key, used to rewrite exactly one row.
    IDColumn string
    // LabelColumn is a human-readable name of the row (a conn_id), for messages.
    LabelColumn string
    // Prefix, when set, tags the values of the column that are ciphertext; a
    // value without it is plaintext and not a secret value of the registry.
    // Variables hold both forms in one column (#507). SecretValue.Ciphertext
    // carries the value without the prefix, and a rewrite puts it back.
    Prefix string
}
```

<a name="EncryptedColumns"></a>
### func [EncryptedColumns](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L52>)

```go
func EncryptedColumns() []EncryptedColumn
```

EncryptedColumns returns a copy of the encrypted\-column registry.

<a name="EncryptedColumn.String"></a>
### func \(EncryptedColumn\) [String](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L34>)

```go
func (c EncryptedColumn) String() string
```

String renders the column as table.column.

<a name="ExecutionStore"></a>
## type [ExecutionStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L24-L30>)

ExecutionStore resolves task execution context from Postgres. It implements both agentrpc.Store \(serving the in\-pod agent\) and dispatch.Resolver \(feeding the pod\-path dispatcher\) over the same dag\_version spec.

```go
type ExecutionStore struct {
    // contains filtered or unexported fields
}
```

<a name="NewExecutionStore"></a>
### func [NewExecutionStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L40>)

```go
func NewExecutionStore(pg *Postgres) *ExecutionStore
```

NewExecutionStore builds an ExecutionStore over the given Postgres connection.

<a name="ExecutionStore.BindWarmAttempt"></a>
### func \(\*ExecutionStore\) [BindWarmAttempt](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L310>)

```go
func (s *ExecutionStore) BindWarmAttempt(ctx context.Context, runID, taskID string, tryNumber, attemptEpoch int, workerPod string) error
```

BindWarmAttempt records the durable warm\-attempt binding \(ADR 0058 N1d\-a1\): the warm worker pod \(workerPod, its own downward\-API pod name\) that acked this attempt as started, stamped onto warm\_worker\_id so a later failover reaper can tell which running attempts a dead warm pod held. The UPDATE is guarded on state IN \('scheduled', 'queued', 'running'\), so a settled attempt is never bound \(an ack that races a reaper settling the row is a benign no\-op, zero rows, not an error\), while an ack that lands before the tick records queued still binds \(\#1600\). It is written ONLY on a warm ack; a dedicated\-pod attempt \(and every attempt while warm pools are off\) leaves warm\_worker\_id NULL.

N1d\-a2 deferral: warm\_worker\_id is intentionally NOT cleared when the attempt settles. The consuming reaper filters on state, so a lingering value on a terminal TI is harmless; a settle\-time clear is left to that increment.

<a name="ExecutionStore.FailTask"></a>
### func \(\*ExecutionStore\) [FailTask](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L364>)

```go
func (s *ExecutionStore) FailTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string) error
```

FailTask marks a task instance failed by its ID, guarded by the attempt \(try\_number and attempt\_epoch, from the pod's labels\) and the active states so it never clobbers a different attempt or a terminal row. It implements part of executor.OutcomeReporter for the pod reconciler \(ADR 0052\).

<a name="ExecutionStore.FailTaskInfra"></a>
### func \(\*ExecutionStore\) [FailTaskInfra](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L383>)

```go
func (s *ExecutionStore) FailTaskInfra(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string) error
```

FailTaskInfra marks a task instance failed for an infrastructure reason by its ID \(last\_failure\_kind 'infra', confirmed\), guarded like FailTask, so the planner re\-places it on the infra budget instead of spending a task retry. It implements part of executor.OutcomeReporter for a pod the cluster stopped \(ADR 0051, \#1595\).

<a name="ExecutionStore.IsTaskInstanceLive"></a>
### func \(\*ExecutionStore\) [IsTaskInstanceLive](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L346>)

```go
func (s *ExecutionStore) IsTaskInstanceLive(ctx context.Context, id auth.AgentIdentity) (bool, error)
```

IsTaskInstanceLive reports whether the attempt id names \(run, task, try and attempt epoch; a token without an epoch is matched on its try alone\) is still live \(present and in an active, non\-terminal state\), derived from the same predicate RecordHeartbeat writes on, but as a pure read with no side\-effect \(ADR 0055\). A scheduled attempt also counts when the token carries the epoch the dispatch claimed: ClaimAttemptEpoch bumps the epoch before the instance turns queued, so a task that starts fast can ask for a secret in that gap \(\#2201\). A token without an epoch never matches that state. It is the read\-only revocation signal the secret path consults: a terminal, superseded \(try\_number moved on\), or reaped attempt is not live, so its token stops resolving secrets even while the signature holds.

It derives ONLY from \(run, task, try, epoch\) \+ active state, exactly as the heartbeat predicate does. It must never gain a run\-recency / logical\_date clause: a recency term would deny a legitimate clear\-and\-rerun of an old run, binding credential lifetime to the run's age rather than to the attempt.

<a name="ExecutionStore.OnPool"></a>
### func \(\*ExecutionStore\) [OnPool](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L47>)

```go
func (s *ExecutionStore) OnPool(pg *Postgres) *ExecutionStore
```

OnPool returns a copy of s that runs its queries on pg's pool and keeps everything else, the spec cache and the observers included, so moving the agent handlers to their own pool \(database.agent\_max\_conns, \#1624\) loses no metric.

<a name="ExecutionStore.RecordHeartbeat"></a>
### func \(\*ExecutionStore\) [RecordHeartbeat](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L271>)

```go
func (s *ExecutionStore) RecordHeartbeat(ctx context.Context, id auth.AgentIdentity) error
```

RecordHeartbeat stamps last\_heartbeat\_at on the agent's TI so the scheduler's heartbeat reaper \(\#128\) can tell a live task from one whose agent has gone silent. The SQL guard skips terminal rows — a late heartbeat after a terminal report is a no\-op, not a regression.

<a name="ExecutionStore.ReportState"></a>
### func \(\*ExecutionStore\) [ReportState](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L205>)

```go
func (s *ExecutionStore) ReportState(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string) error
```

ReportState records a state transition reported by the agent, persisting the exit code and error message and stamping started/ended/duration timestamps.

<a name="ExecutionStore.RequeueForRedispatch"></a>
### func \(\*ExecutionStore\) [RequeueForRedispatch](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L484>)

```go
func (s *ExecutionStore) RequeueForRedispatch(ctx context.Context, runID, taskID string, tryNumber, attemptEpoch int) error
```

RequeueForRedispatch re\-places a reclaimed warm assignment \(ADR 0058 N1d\-c, H2\): an attempt that a warm worker was handed but demonstrably will not run \(its stream ended holding an unacked lease, or it acked started=false\) sits \`queued\` having never run. This moves it back to \`scheduled\` so the planner re\-admits and re\-dispatches it, rather than leaving it stuck until the 3\-minute dispatch\-lost reaper. Guarded to state='queued' and bounded to the exact attempt \(runID, taskID, tryNumber\): zero rows is the guard working \(the TI is running or settled, or the attempt moved on\), a benign no\-op, not an error. It bumps neither try\_number nor infra\_attempts — the attempt never ran, this is a re\-offer of the same attempt.

<a name="ExecutionStore.Reschedule"></a>
### func \(\*ExecutionStore\) [Reschedule](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L245>)

```go
func (s *ExecutionStore) Reschedule(ctx context.Context, id auth.AgentIdentity, at time.Time) error
```

Reschedule parks an active task instance in up\_for\_reschedule with its next\-poke time, so the scheduler re\-dispatches it once reschedule\_at passes \(\#380\). Used by the agent's reschedule path. It is guarded on the attempt \(try\_number and attempt epoch, ADR 0051 amendment\) and the active states, so a poke from a superseded attempt or onto a settled row does not apply and returns agentrpc.ErrStaleReport.

<a name="ExecutionStore.RescheduleTask"></a>
### func \(\*ExecutionStore\) [RescheduleTask](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L461>)

```go
func (s *ExecutionStore) RescheduleTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, at time.Time) error
```

RescheduleTask parks a task instance in up\_for\_reschedule with the recovered next\-poke time, guarded by the attempt and the active states, consuming no retry budget \(ADR 0052\). Used by the reconciler when a reschedule report was lost.

<a name="ExecutionStore.ResolveTask"></a>
### func \(\*ExecutionStore\) [ResolveTask](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L558>)

```go
func (s *ExecutionStore) ResolveTask(ctx context.Context, runID, taskID string) (dispatch.Resolved, error)
```

ResolveTask returns the dispatcher's execution context for a run's task and claims a fresh attempt epoch for the execution it is about to start \(ADR 0051 amendment\). Every call that succeeds moves the row's attempt\_epoch forward, so two dispatches of one try never share it. The agent's identity \(A2\) and the pod's attempt\-epoch label \(A4\) are minted from the returned value. A row past dispatch is refused with ErrNotDispatchable and keeps its epoch.

<a name="ExecutionStore.SetQueueWaitObserver"></a>
### func \(\*ExecutionStore\) [SetQueueWaitObserver](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L35>)

```go
func (s *ExecutionStore) SetQueueWaitObserver(fn func(tenant string, seconds float64))
```

SetQueueWaitObserver installs fn to receive, once per attempt, the seconds from queued\_at to the agent's first running report, with the tenant name. Call it before the store serves reports.

<a name="ExecutionStore.SettledRuns"></a>
### func \(\*ExecutionStore\) [SettledRuns](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L644>)

```go
func (s *ExecutionStore) SettledRuns(ctx context.Context, refs []executor.RunRef) (map[executor.RunRef]bool, error)
```

SettledRuns reports which of the given \(tenant, run\) pairs are settled: run in success or failed and no task instance outside success, failed, skipped and upstream\_failed. It serves the reconciler's settled\-run pod collection. A pair that is not two UUIDs, names no such run, or names a run of another tenant is left out, so its pods are never collected early.

<a name="ExecutionStore.SucceedTask"></a>
### func \(\*ExecutionStore\) [SucceedTask](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L400>)

```go
func (s *ExecutionStore) SucceedTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) error
```

SucceedTask marks a task instance succeeded by its ID — recovering a success whose report was lost \(ADR 0052\) — guarded by the attempt and the active states. A settle on an already\-terminal or superseded row is a no\-op.

<a name="ExecutionStore.SucceedTaskIfActive"></a>
### func \(\*ExecutionStore\) [SucceedTaskIfActive](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L409>)

```go
func (s *ExecutionStore) SucceedTaskIfActive(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
```

SucceedTaskIfActive is SucceedTask reporting whether it changed the row. A false with no error means the attempt was not active: already terminal, superseded, or marked by a reaper meanwhile. It implements part of executor.InfraOverrideReporter.

<a name="ExecutionStore.SucceedTaskOverInfraMark"></a>
### func \(\*ExecutionStore\) [SucceedTaskOverInfraMark](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L432>)

```go
func (s *ExecutionStore) SucceedTaskOverInfraMark(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (executor.InfraOverride, bool, error)
```

SucceedTaskOverInfraMark settles a durable SUCCESS record over a reaper's provisional infra mark of the same attempt \(ADR 0052 amendment, part 1\). It applies only while the mark is unconfirmed and inside scheduler.InfraConfirmMaxWait, on a running run; otherwise it changes nothing and reports false. On success it returns the mark it overrode and the attempt's log location. It implements part of executor.InfraOverrideReporter.

<a name="ExecutionStore.TaskSpec"></a>
### func \(\*ExecutionStore\) [TaskSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/agent_store.go#L54>)

```go
func (s *ExecutionStore) TaskSpec(ctx context.Context, id auth.AgentIdentity) (agentrpc.TaskSpec, error)
```

TaskSpec returns the agent\-facing execution spec for a task instance.

<a name="KeyMigrationHooks"></a>
## type [KeyMigrationHooks](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L174-L181>)

KeyMigrationHooks are named step boundaries inside the migration transaction. Production leaves them nil; the crash\-injection tests stop the process at each one \(ADR 0065 section 9\).

```go
type KeyMigrationHooks struct {
    // AfterFirstUpdate runs once, after the first UPDATE.
    AfterFirstUpdate func()
    // AfterUpdates runs after every UPDATE, before the in-transaction check.
    AfterUpdates func()
    // AfterVerify runs after the in-transaction check, before COMMIT.
    AfterVerify func()
}
```

<a name="KeyMigrationResult"></a>
## type [KeyMigrationResult](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L154-L165>)

KeyMigrationResult counts what one migration transaction did.

```go
type KeyMigrationResult struct {
    // Migrated is the number of values rewritten under the encrypting key.
    Migrated int
    // Skipped is the number of rewrites the optimistic guard matched to zero
    // rows. Under the table lock it cannot happen, so it fails the pass.
    Skipped int
    // Unreadable is the number of values no given key opens.
    Unreadable int
    // Verified is the number of values that opened under the encrypting key
    // alone, with the plaintext read at the start, before COMMIT.
    Verified int
}
```

<a name="MigrateSecretValues"></a>
### func [MigrateSecretValues](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L225>)

```go
func MigrateSecretValues(ctx context.Context, db TxBeginner, enc secrets.Cipher, predecessors []secrets.Cipher, hooks KeyMigrationHooks) (KeyMigrationResult, error)
```

MigrateSecretValues moves every encrypted value that a predecessor opens onto enc, in ONE transaction that commits only after a clean, verified pass \(ADR 0065 step 4\).

Under SHARE ROW EXCLUSIVE locks on every registered table, it reads every value, opens each with enc and then the predecessors, rewrites the ones a predecessor opened, then re\-reads every value and requires each to open under enc ALONE with the plaintext read at the start. Any of: a value no key opens, an UPDATE that matched no row, a value that fails the check, rolls the transaction back and returns an error with the counts so far. Nothing is ever committed from a partial pass.

<a name="KeyMigrationResult.Clean"></a>
### func \(KeyMigrationResult\) [Clean](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L169>)

```go
func (r KeyMigrationResult) Clean() bool
```

Clean reports whether the pass left nothing behind: no value skipped and none unreadable. Anything else is an incomplete pass, never "nothing happened".

<a name="LogReader"></a>
## type [LogReader](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/log_reader.go#L17-L21>)

LogReader resolves a task attempt's log location from API\-facing identifiers and reads it from the log sink, and tails its live lines.

```go
type LogReader struct {
    // contains filtered or unexported fields
}
```

<a name="NewLogReader"></a>
### func [NewLogReader](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/log_reader.go#L25>)

```go
func NewLogReader(pg *Postgres, sink logs.Sink, tailer logs.Tailer) *LogReader
```

NewLogReader builds a LogReader over the given Postgres connection, sink, and live\-tail tailer \(tailer may be nil to disable following\).

<a name="LogReader.ReadLogs"></a>
### func \(\*LogReader\) [ReadLogs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/log_reader.go#L71>)

```go
func (r *LogReader) ReadLogs(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (io.ReadCloser, error)
```

ReadLogs resolves the run reference \(tenant name \-\> id, run\_id \-\> dag\_run id\), then opens the stored log for the task attempt. It returns domain.ErrNotFound when the run or its log file is absent. See issue \#21 for the resolution cost.

The API addresses a try, and one try can have several executions \(an infra re\-place, a reschedule poke, a repeated dispatch\), each storing its stream under its own attempt epoch \(\#863\). The epochs the try can span come from the database \(TryAttemptEpochBounds\), and logs.ReadAttempts serves every stored one in order, listing the stored executions in one call where the sink can and opening each stream only as the read reaches it. A try that ran once reads exactly as before.

<a name="LogReader.Tail"></a>
### func \(\*LogReader\) [Tail](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/log_reader.go#L32>)

```go
func (r *LogReader) Tail(ctx context.Context, tenant, dagID, runID, taskID string, tryNumber int) (lines <-chan string, cancel func(), err error)
```

Tail subscribes to the task attempt's live log lines, returning a line channel and a cancel function. It resolves the run reference the same way ReadLogs does so the channel matches what the agent publishes.

<a name="Option"></a>
## type [Option](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository_options.go#L11>)

Option configures a Repository at construction \(\#1949\). Each option documents what a Repository built without it does.

```go
type Option func(*repoConfig)
```

<a name="WithCipher"></a>
### func [WithCipher](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository_options.go#L36>)

```go
func WithCipher(c secrets.Cipher) Option
```

WithCipher sets the encryption cipher used for connection secrets \(ADR 0019\) and sealed Variable values \(\#507\). Without it, connection writes fail rather than storing plaintext.

<a name="WithDagsArePausedAtCreation"></a>
### func [WithDagsArePausedAtCreation](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository_options.go#L58>)

```go
func WithDagsArePausedAtCreation(paused bool) Option
```

WithDagsArePausedAtCreation sets whether a DAG registered for the first time starts paused when its spec has no is\_paused\_upon\_creation \(the server's dags\_are\_paused\_at\_creation, \#1705\). It never affects a DAG that exists. Without it, new DAGs are registered active.

<a name="WithExternalSecretCoverage"></a>
### func [WithExternalSecretCoverage](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository_options.go#L50>)

```go
func WithExternalSecretCoverage(c externalSecretCoverage) Option
```

WithExternalSecretCoverage sets the operator\-configured external\-backend coverage used by the D6 registration check \(ADR 0060 B1\). Without it, the check is the pre\-0060 vault\-existence check \(a declared name must exist in the vault\). It must be wired from operator/Helm config, never from a DAG.

<a name="WithVariableEncryption"></a>
### func [WithVariableEncryption](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository_options.go#L42>)

```go
func WithVariableEncryption(m VariableEncryption) Option
```

WithVariableEncryption sets how Variable values are written and which way the boot sweep converges them. The default is off.

<a name="WithXComBackend"></a>
### func [WithXComBackend](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository_options.go#L65>)

```go
func WithXComBackend(b XComValueDeleter) Option
```

WithXComBackend sets the XCom value store, so a clear deletes the values of the attempts it clears and not only their index rows \(\#1131\). Without it a clear removes only the index rows.

<a name="PoolCollector"></a>
## type [PoolCollector](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_metrics.go#L26-L31>)

PoolCollector exports pgxpool statistics of the control plane's database pools, read at scrape time \(\#1049\).

The health pool \(\#1046\) keeps readiness green while the request pool is fully lent out, which is the right readiness semantic and removes the last symptom of main\-pool exhaustion. These series are the signal that replaces it: dexaflow\_db\_pool\_empty\_acquires\_total counts callers that found no idle connection and had to wait, and dexaflow\_db\_pool\_canceled\_acquires\_total the ones that gave up waiting.

```go
type PoolCollector struct {
    // contains filtered or unexported fields
}
```

<a name="NewPoolCollector"></a>
### func [NewPoolCollector](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_metrics.go#L36>)

```go
func NewPoolCollector(pg *Postgres) *PoolCollector
```

NewPoolCollector returns a collector for pg's main and health pools, and for its dedicated scheduler and agent pools while they are open \(ForScheduler, ForAgents\).

<a name="PoolCollector.Collect"></a>
### func \(\*PoolCollector\) [Collect](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_metrics.go#L60>)

```go
func (c *PoolCollector) Collect(ch chan<- prometheus.Metric)
```

Collect implements prometheus.Collector.

<a name="PoolCollector.Describe"></a>
### func \(\*PoolCollector\) [Describe](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_metrics.go#L53>)

```go
func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc)
```

Describe implements prometheus.Collector.

<a name="Postgres"></a>
## type [Postgres](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/postgres.go#L37-L77>)

Postgres holds a pgx connection pool and the generated query set.

```go
type Postgres struct {
    Pool    *pgxpool.Pool
    Queries *queries.Queries
    // contains filtered or unexported fields
}
```

<a name="NewPostgres"></a>
### func [NewPostgres](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/postgres.go#L193>)

```go
func NewPostgres(ctx context.Context, cfg config.DatabaseSection) (*Postgres, error)
```

NewPostgres opens a connection pool and verifies connectivity, retrying transient failures during boot for up to pgStartupBudget. Pre\-2026\-06, the first failed ping fatal\-ed the server, so a docker compose race or any Pro failover blip became a hard crash. The retry loop keeps Lite boot ergonomic and Pro startup resilient under realistic upstream\-PG dynamics. A truly broken setup \(wrong DSN, bad auth\) still surfaces quickly because the underlying error is wrapped into the final error.

<a name="Postgres.CheckRowSecurity"></a>
### func \(\*Postgres\) [CheckRowSecurity](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/tenant_binding.go#L110>)

```go
func (p *Postgres) CheckRowSecurity(ctx context.Context) error
```

CheckRowSecurity returns ErrRowSecurityOff, naming the tables, unless every tenant table has row level security on, forced and with its policy.

<a name="Postgres.Close"></a>
### func \(\*Postgres\) [Close](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/postgres.go#L501>)

```go
func (p *Postgres) Close()
```

Close releases the connection pools.

<a name="Postgres.ForAgents"></a>
### func \(\*Postgres\) [ForAgents](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/postgres.go#L463>)

```go
func (p *Postgres) ForAgents(ctx context.Context, cfg config.DatabaseSection) (*Postgres, func(), error)
```

ForAgents returns the handle the agent gRPC handlers should use, and a func that releases it \(\#1624\).

With database.agent\_max\_conns unset it is p itself and the release func does nothing: heartbeats and state reports share the main pool with the API, as they always have. With it set, it is a dedicated pool of that size, without the API statement timeout, so API traffic that holds every main pool connection cannot block a heartbeat write while the reapers, on the scheduler pool, read last\_heartbeat\_at and judge the agent silent. The dedicated handle shares p's spec cache.

<a name="Postgres.ForScheduler"></a>
### func \(\*Postgres\) [ForScheduler](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/postgres.go#L442>)

```go
func (p *Postgres) ForScheduler(ctx context.Context, cfg config.DatabaseSection) (*Postgres, func(), error)
```

ForScheduler returns the handle the scheduler side should use, and a func that releases it.

With database.scheduler\_max\_conns unset it is p itself and the release func does nothing: the scheduler shares the main pool, as it always has. With it set, it is a dedicated pool of that size, without the API statement timeout, so a burst of API traffic holding every main pool connection cannot stall a scheduler tick or a reaper behind it. The dedicated handle shares p's spec cache, so the scheduler and the agent path still decode each version once.

<a name="Postgres.Ping"></a>
### func \(\*Postgres\) [Ping](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/postgres.go#L496>)

```go
func (p *Postgres) Ping(ctx context.Context) error
```

Ping checks database connectivity \(used by /readyz\). It goes through probePool, not Pool: a connectivity check that has to wait for the request path to free a connection is reporting load, not connectivity.

<a name="Postgres.SchemaReady"></a>
### func \(\*Postgres\) [SchemaReady](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/schema_check.go#L223>)

```go
func (p *Postgres) SchemaReady(ctx context.Context) error
```

SchemaReady is the readiness probe's entry point: it re\-asserts on every probe what boot asserted once, because a boot check says nothing about a database that changes UNDER a running pod.

It exists because a Ping only proves the connection. A database emptied by an ephemeral\-volume recycle, restored from an older backup, failed over to a lagging replica, or repointed by a changed database.url stays pingable while serving nothing \(\#1023\). One SELECT against a single\-row table, so it is cheap enough to run per probe; the caller bounds it with the probe's own deadline.

The verdict is checkSchemaReady, NOT the boot gate; see there for the one state where the two must disagree.

<a name="Postgres.SchemaVersion"></a>
### func \(\*Postgres\) [SchemaVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/schema_check.go#L18>)

```go
func (p *Postgres) SchemaVersion(ctx context.Context) (version uint, dirty, exists bool, err error)
```

SchemaVersion reads the applied migration version from golang\-migrate's schema\_migrations table. exists is false when the table is absent \(migrations never ran\). It issues only a SELECT, so it is compatible with the read\-only role:api DB identity \(ADR 0049\), which skips migrations by design.

<a name="Postgres.SchemaWaitState"></a>
### func \(\*Postgres\) [SchemaWaitState](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/schema_check.go#L304>)

```go
func (p *Postgres) SchemaWaitState(ctx context.Context) (SchemaWait, error)
```

SchemaWaitState reads the schema and returns the boot gate's verdict \(schemaWaitVerdict\). A read failure is not fatal: it comes back as the SchemaReasonUnreadable class with the cause in Err, so the caller keeps waiting. The only error is a schema dirty at this binary's latest with no migration in flight.

<a name="Postgres.SetOnReplaced"></a>
### func \(\*Postgres\) [SetOnReplaced](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/schema_check.go#L354>)

```go
func (p *Postgres) SetOnReplaced(f func())
```

SetOnReplaced registers what a readiness probe calls when it finds the database replaced under this process \(ADR 0090 decision 5\); the server stops on it. Set it once, at boot, before the probes run.

<a name="Postgres.SetRecoveryHint"></a>
### func \(\*Postgres\) [SetRecoveryHint](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/schema_check.go#L279>)

```go
func (p *Postgres) SetRecoveryHint(hint func(class string) string)
```

SetRecoveryHint registers what a schema readiness failure says to run for each reason class, so a running pod's log names the same command for its install method as the boot gate \(ADR 0090 decision 2\). Set it once, at boot, before the probes run.

<a name="Postgres.SetSchemaObserver"></a>
### func \(\*Postgres\) [SetSchemaObserver](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/schema_check.go#L349>)

```go
func (p *Postgres) SetSchemaObserver(observe func(class string))
```

SetSchemaObserver registers what readiness reports each schema verdict to: the class of a not\-ready schema, or "" when it is ready. It feeds dexaflow\_schema\_ready for a pod that was already serving. Set it once, at boot, before the probes run.

<a name="Redis"></a>
## type [Redis](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis.go#L17-L19>)

Redis wraps a go\-redis client used for XCom values and the live log tail.

```go
type Redis struct {
    Client *redis.Client
}
```

<a name="NewRedis"></a>
### func [NewRedis](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis.go#L57>)

```go
func NewRedis(ctx context.Context, cfg config.RedisSection) (*Redis, error)
```

NewRedis connects to Redis and verifies connectivity.

<a name="Redis.Close"></a>
### func \(\*Redis\) [Close](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis.go#L75>)

```go
func (r *Redis) Close() error
```

Close releases the Redis client.

<a name="Redis.MemoryInfo"></a>
### func \(\*Redis\) [MemoryInfo](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_memory.go#L40>)

```go
func (r *Redis) MemoryInfo(ctx context.Context) (RedisMemoryInfo, error)
```

MemoryInfo reads the eviction policy, the used memory and the evicted keys from INFO, which managed services allow where CONFIG GET is blocked. The sections are read one call each because Redis before 7 takes one section per INFO.

<a name="Redis.Ping"></a>
### func \(\*Redis\) [Ping](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis.go#L70>)

```go
func (r *Redis) Ping(ctx context.Context) error
```

Ping checks Redis connectivity \(used by /readyz\).

<a name="RedisMemoryInfo"></a>
## type [RedisMemoryInfo](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_memory.go#L21-L29>)

RedisMemoryInfo is what the server reports about its memory and evictions.

```go
type RedisMemoryInfo struct {
    // Policy is maxmemory_policy, for example noeviction or volatile-lru.
    Policy string
    // UsedMemory is used_memory, in bytes.
    UsedMemory int64
    // EvictedKeys is evicted_keys: keys the server deleted to stay under
    // maxmemory since it started.
    EvictedKeys int64
}
```

<a name="CheckRedisEviction"></a>
### func [CheckRedisEviction](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_memory.go#L97>)

```go
func CheckRedisEviction(ctx context.Context, r *Redis, require bool, logger *slog.Logger) (RedisMemoryInfo, error)
```

CheckRedisEviction is the boot check of the eviction policy \(\#2354\). A policy other than noeviction lets Redis delete the XCom values of running DAGs under memory pressure, and the downstream task then fails with "not found". It logs a warning that names the risk, or, when require is set \(redis.require\_noeviction\), returns ErrRedisEvictionUnsafe so the process refuses to boot. A policy that cannot be read is a warning, or a refusal in strict mode: strict mode fails closed.

<a name="RedisMemoryInfo.EvictionSafe"></a>
### func \(RedisMemoryInfo\) [EvictionSafe](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_memory.go#L32>)

```go
func (i RedisMemoryInfo) EvictionSafe() bool
```

EvictionSafe reports whether the policy keeps every key until it expires.

<a name="RedisMemoryMetrics"></a>
## type [RedisMemoryMetrics](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_memory.go#L81-L83>)

RedisMemoryMetrics receives what WatchRedisMemory reads from INFO. It is a local interface so this package does not import internal/observability.

```go
type RedisMemoryMetrics interface {
    SetRedisMemory(evictionSafe bool, usedBytes, evictedKeys int64)
}
```

<a name="RedisMetrics"></a>
## type [RedisMetrics](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_observability.go#L18-L24>)

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

<a name="RedisRateCounter"></a>
## type [RedisRateCounter](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_ratelimit.go#L15-L17>)

RedisRateCounter is the auth.SharedCounter of the API replicas on Redis \(\#2357\): one INCR and EXPIRE per counted event, in one round trip, so every replica behind the Service spends the same budget.

```go
type RedisRateCounter struct {
    // contains filtered or unexported fields
}
```

<a name="NewRedisRateCounter"></a>
### func [NewRedisRateCounter](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_ratelimit.go#L20>)

```go
func NewRedisRateCounter(client *redis.Client) *RedisRateCounter
```

NewRedisRateCounter builds the shared rate limit counter over client.

<a name="RedisRateCounter.Decr"></a>
### func \(\*RedisRateCounter\) [Decr](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_ratelimit.go#L56>)

```go
func (r *RedisRateCounter) Decr(ctx context.Context, key string) error
```

Decr gives one event of key back.

<a name="RedisRateCounter.Get"></a>
### func \(\*RedisRateCounter\) [Get](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_ratelimit.go#L44>)

```go
func (r *RedisRateCounter) Get(ctx context.Context, key string) (int64, error)
```

Get returns the count of key, 0 when it has none.

<a name="RedisRateCounter.Incr"></a>
### func \(\*RedisRateCounter\) [Incr](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/redis_ratelimit.go#L33>)

```go
func (r *RedisRateCounter) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
```

Incr counts one event of key and returns the count. key expires after ttl, which outlives the window the key names, since the key is created inside it.

<a name="ReencryptResult"></a>
## type [ReencryptResult](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/reencrypt.go#L13-L21>)

ReencryptResult counts what one boot\-time re\-encryption pass did.

```go
type ReencryptResult struct {
    // Migrated is the number of rows rewritten under the primary key.
    Migrated int
    // Skipped is the number of rows a concurrent writer changed between the
    // pass's read and its write; they were left to that writer.
    Skipped int
    // Unreadable is the number of columns no configured key opens.
    Unreadable int
}
```

<a name="ReencryptResult.Clean"></a>
### func \(ReencryptResult\) [Clean](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/reencrypt.go#L25>)

```go
func (r ReencryptResult) Clean() bool
```

Clean reports whether the pass moved everything it found: nothing skipped and nothing unreadable. Only a clean pass completes a rotation.

<a name="Repository"></a>
## type [Repository](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository.go#L29-L49>)

Repository implements the API resource and auth user\-store interfaces over Postgres using the sqlc\-generated query set.

```go
type Repository struct {
    // contains filtered or unexported fields
}
```

<a name="NewRepository"></a>
### func [NewRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository.go#L72>)

```go
func NewRepository(pg *Postgres, opts ...Option) *Repository
```

NewRepository builds a Repository backed by the given Postgres connection, configured by opts. Every option is optional here: tools and tests that need none of them get the zero behavior each option documents. The server builds its repository through NewServerRepository, which also checks that the dependencies it must decide were given.

<a name="NewServerRepository"></a>
### func [NewServerRepository](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository_options.go#L83>)

```go
func NewServerRepository(pg *Postgres, opts ...Option) (*Repository, error)
```

NewServerRepository builds the control plane's Repository. Unlike NewRepository it refuses to build one whose missing options would silently change behavior: without an XCom backend a clear leaves the values of the cleared attempts behind, and without dags\_are\_paused\_at\_creation new DAGs start active where the server default pauses them. The cipher, Variable encryption and external\-secret coverage stay optional, because running without each is a supported configuration the server already reports.

<a name="Repository.AddFavorite"></a>
### func \(\*Repository\) [AddFavorite](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L286>)

```go
func (r *Repository) AddFavorite(ctx context.Context, tenant, userID, dagID string) error
```

AddFavorite marks a DAG as a favorite for the user \(idempotent\).

<a name="Repository.AdminUserState"></a>
### func \(\*Repository\) [AdminUserState](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L185>)

```go
func (r *Repository) AdminUserState(ctx context.Context, tenant, email string) (auth.AdminState, error)
```

AdminUserState reports whether a user with exactly this email exists in the tenant, active or not and whatever its login, and whether it holds the admin role. It backs the bootstrap admin resolution \(auth.ResolveAdminEmail\): an install whose admin predates the rename to Dexaflow keeps that admin instead of gaining a second one.

<a name="Repository.AlertEndpoint"></a>
### func \(\*Repository\) [AlertEndpoint](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L185>)

```go
func (r *Repository) AlertEndpoint(ctx context.Context, tenantID, connID string) (endpointURL string, headers map[string]string, err error)
```

AlertEndpoint resolves an alert channel connection \(\#424\) to its endpoint for a tenant UUID: the decrypted password is the channel URL \(the full webhook URL, kept encrypted at rest\), and an optional \`headers\` object in the connection's extra becomes request headers \(e.g. an Authorization header for an endpoint whose token is not in the URL\). An absent/empty URL is an error, a misconfigured alert connection must fail loud. Never expose these in UI/API.

<a name="Repository.BootstrapAdmin"></a>
### func \(\*Repository\) [BootstrapAdmin](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L275>)

```go
func (r *Repository) BootstrapAdmin(ctx context.Context, tenant, email, password string) (bool, error)
```

BootstrapAdmin creates a default admin user with the given password when the tenant has no users yet, assigning the seeded admin role. It returns whether a user was created \(false when users already exist\).

<a name="Repository.BootstrapAdminHash"></a>
### func \(\*Repository\) [BootstrapAdminHash](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L471>)

```go
func (r *Repository) BootstrapAdminHash(ctx context.Context, tenant, email, hash string) (bool, error)
```

BootstrapAdminHash provisions the Lite admin from a precomputed bcrypt hash \(so the plaintext never reaches the control plane\). It RECONCILES: if the admin already exists, its password is reset to this hash. The Lite config \(admin\_password\_hash\) is the source of truth, so the password the setup printed always logs in, even against a pre\-existing or stale database, without anyone having to wipe Docker volumes. The only sanctioned way to change the password, \`reset\-password\`, also writes the config, so the two never drift. Every path also grants the admin role of the admin's own tenant, idempotently: a new admin is created and granted in one transaction, and an existing admin without the role \(left by a crash between the two steps in an older release, \#513\) gets it back. Returns true only when the admin was newly created \(false when an existing one was reconciled\). See internal/serverapp bootstrapAdmin.

<a name="Repository.ClearDagHistory"></a>
### func \(\*Repository\) [ClearDagHistory](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L317>)

```go
func (r *Repository) ClearDagHistory(ctx context.Context, tenant, dagID string) error
```

ClearDagHistory deletes a DAG's runs \(cascading task instances and XCom index rows\) while keeping the DAG and its versions registered, the safe "clear" the UI trash maps to \(ADR 0020\). Returns ErrNotFound when the DAG is absent.

<a name="Repository.ClearImportError"></a>
### func \(\*Repository\) [ClearImportError](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L593>)

```go
func (r *Repository) ClearImportError(ctx context.Context, tenant, filename string) error
```

ClearImportError removes any recorded error for a file \(a good re\-import\).

<a name="Repository.ClearTaskInstances"></a>
### func \(\*Repository\) [ClearTaskInstances](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L395>)

```go
func (r *Repository) ClearTaskInstances(ctx context.Context, tenant, dagID, runID string, taskIDs []string, onlyFailed bool, opts domain.ClearOptions) (int, error)
```

ClearTaskInstances resets tasks to none for re\-run. When onlyFailed is true, only tasks currently in a failed\-ish state \(failed, upstream\_failed, up\_for\_retry\) are reset; with an empty taskIDs and onlyFailed, every failed task in the run is cleared. It returns the number of task instances actually reset.

opts carries the two independent run\-level decisions, whether to re\-open the run, and which version the re\-run executes \(see domain.ClearOptions\).

A clear that asks for the current version \(opts.RunOnLatestVersion with opts.ResetDagRun\) moves the run to it only when the run has finished and none of its task instances is queued or running, decided inside the clear's transaction on the locked run \(decideMove, ADR 0086\). The move brings the run's task instances to that version's tasks \(alignTaskInstances\). Any other run keeps its version, and the clear applies on it; RunsKeepingVersion reports why. Either way the clear resets only tasks the version the re\-run executes declares, so a task instance left removed by an earlier move stays removed.

Each cleared task instance gets its retry budget back and starts without the XCom of the attempt it replaces \(\#1131\). The whole clear is one transaction, and the stored XCom values are deleted before it commits: a backend failure rolls the clear back rather than re\-queueing a task whose previous values are still readable.

<a name="Repository.ConvergeVariableEncryption"></a>
### func \(\*Repository\) [ConvergeVariableEncryption](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/variable_crypto.go#L156>)

```go
func (r *Repository) ConvergeVariableEncryption(ctx context.Context) (VariableSweepResult, error)
```

ConvergeVariableEncryption rewrites stored variable values into the form the mode asks for: "on" seals every plaintext row, "decrypt" restores every sealed row to plaintext, and "off" changes nothing. It is idempotent and safe on every boot and from several replicas at once: each rewrite is guarded on the value it read. A sealed row no key opens is left untouched and reported, because its ciphertext is the only copy of that value.

<a name="Repository.CreateAPIToken"></a>
### func \(\*Repository\) [CreateAPIToken](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/api_tokens.go#L32>)

```go
func (r *Repository) CreateAPIToken(ctx context.Context, tenant, userID, name, hash string, scopes []string, expiresAt time.Time) (auth.APIToken, error)
```

CreateAPIToken stores a new API token for userID, a user of tenant, under name, keeping only hash \(ADR 0074\). It runs in one transaction that locks the user row, so the MaxLiveAPITokensPerUser cap holds under concurrent creates, and that first revokes the user's expired tokens of the same name, so an expired token does not hold its name. Each token it retires that way gets an api\_token.retire\_expired audit row in the same transaction, naming when it expired and the token that took its name \(\#1768\). A user outside the tenant, or one at the cap, is domain.ErrValidation; a live token of the same owner and name is domain.ErrConflict.

<a name="Repository.CreateCLIDeviceAuthorization"></a>
### func \(\*Repository\) [CreateCLIDeviceAuthorization](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/cli_device.go#L38>)

```go
func (r *Repository) CreateCLIDeviceAuthorization(ctx context.Context, req auth.DeviceAuthorizationRequest) (auth.DeviceAuthorization, error)
```

CreateCLIDeviceAuthorization starts a device sign\-in of the CLI \(ADR 0081\) that expires req.TTL from now on the database's clock and may be polled every req.Interval. It stores only the hashes and the requester's address. In one transaction serialized with every other start, it forgets sign\-ins that expired a while ago and refuses with auth.ErrDeviceSignInsFull when req.MaxLive are live, or auth.ErrDeviceSignInsFromRequester when the requester holds req.MaxLivePerRequester, so the caps hold exactly. A start that cannot take its turn within cliDeviceStartLockTimeout is auth.ErrDeviceStartsBusy. A user code hash already in use is domain.ErrConflict: the caller draws another.

<a name="Repository.CreateDagRun"></a>
### func \(\*Repository\) [CreateDagRun](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L210>)

```go
func (r *Repository) CreateDagRun(ctx context.Context, tenant, dagID string, run domain.DagRun) (domain.DagRun, error)
```

CreateDagRun inserts a new run for a DAG at its current version. It does not count the DAG's max\_active\_runs \(\#2426\): as in Airflow, a manual trigger, a scripted backfill or any other caller of the repository is accepted whatever the number of active runs, and a queued run waits until the scheduler's start stage has a free slot under the cap \(\#1766\). Only scheduled creation stops at the cap \(\`Scheduler.hasHeadroom\`\). The tenant's own limits stay refusals: max\_runs\_per\_day, when set, is charged exactly \(createRunWithinDailyLimit\), and a suspended tenant is refused.

<a name="Repository.CreateOIDCUser"></a>
### func \(\*Repository\) [CreateOIDCUser](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L122>)

```go
func (r *Repository) CreateOIDCUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (*auth.User, error)
```

CreateOIDCUser just\-in\-time provisions an OIDC\-only account \(NULL password\), linked by \(oidc\_provider, oidc\_subject\), and grants it the given roles. It mirrors CreateUser's atomicity: every role is resolved BEFORE the insert so an unknown role fails cleanly as domain.ErrValidation without leaving an orphaned account, and the insert plus the grants run in one transaction so a failed grant rolls the account back. An empty role set grants none \(default\-deny\). A concurrent double\-provision surfaces as domain.ErrConflict via the unique \(oidc\_provider, oidc\_subject\) constraint. The returned user carries the granted role names so the caller can mint a token without a reload.

<a name="Repository.CreateUser"></a>
### func \(\*Repository\) [CreateUser](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L299>)

```go
func (r *Repository) CreateUser(ctx context.Context, tenant, email, password string, roles []string) (domain.User, error)
```

CreateUser provisions a new account in the tenant and grants it the given set of roles, returning the created user \(never its password or hash\). It reuses the same bcrypt hashing as the bootstrap admin path \(auth.HashPassword\) and the same email uniqueness guarantee, so a duplicate email surfaces as domain.ErrConflict \(the API maps it to 409\). Every role is resolved BEFORE the insert so an unknown role fails cleanly as domain.ErrValidation without leaving an orphaned account; an empty set grants none, the most restrictive default, leaving the user with no permissions until an admin grants a role.

The insert and the role grants run in a single transaction, so a failure in any grant rolls the user insert back. Without that atomicity a failed grant would leave an account the \(tenant\_id, email\) UNIQUE makes impossible to recreate, every retry would 409 forever with no recovery path.

This backs \`dexaflow auth create\-user\` \(ADR 0008\) and is purely additive: it does not touch the bootstrap/reconcile path.

<a name="Repository.DagRunVersionLabels"></a>
### func \(\*Repository\) [DagRunVersionLabels](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L161>)

```go
func (r *Repository) DagRunVersionLabels(ctx context.Context, tenant, dagID string, runIDs []string) (map[string]string, error)
```

DagRunVersionLabels returns the label of the version each of runIDs is pinned to, keyed by run id, in one query. A run id with no run is absent; a run whose version row is gone maps to "".

<a name="Repository.DagStats"></a>
### func \(\*Repository\) [DagStats](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/dashboard_stats.go#L16>)

```go
func (r *Repository) DagStats(ctx context.Context, tenant string) (domain.DagStats, error)
```

DagStats returns the home dashboard's DAG counters: the total active DAG count plus how many DAGs have a latest run in the failed/running/queued state.

<a name="Repository.DagsArePausedAtCreation"></a>
### func \(\*Repository\) [DagsArePausedAtCreation](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository.go#L83>)

```go
func (r *Repository) DagsArePausedAtCreation() bool
```

DagsArePausedAtCreation reports the setting WithDagsArePausedAtCreation gave.

<a name="Repository.DecideCLIDeviceAuthorization"></a>
### func \(\*Repository\) [DecideCLIDeviceAuthorization](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/cli_device.go#L105>)

```go
func (r *Repository) DecideCLIDeviceAuthorization(ctx context.Context, id string, approve bool, user auth.User) error
```

DecideCLIDeviceAuthorization records the person's answer to a pending, unexpired device sign\-in: approved as user \(its tenant, id, email and the roles the sign\-in resolved\), or canceled. A sign\-in that is not pending, has expired, or, for an approval, a user who is not in user.TenantID, is domain.ErrNotFound and changes nothing, so a sign\-in is decided once.

<a name="Repository.DeleteConnection"></a>
### func \(\*Repository\) [DeleteConnection](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L509>)

```go
func (r *Repository) DeleteConnection(ctx context.Context, tenant, connID string) error
```

DeleteConnection removes a connection, returning ErrNotFound when none matched.

<a name="Repository.DeleteDag"></a>
### func \(\*Repository\) [DeleteDag](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L146>)

```go
func (r *Repository) DeleteDag(ctx context.Context, tenant, dagID string) error
```

DeleteDag removes a DAG and \(via ON DELETE CASCADE\) its versions, runs, task instances, and XCom index rows. It returns ErrNotFound when no DAG matched.

<a name="Repository.DeleteDagRun"></a>
### func \(\*Repository\) [DeleteDagRun](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L187>)

```go
func (r *Repository) DeleteDagRun(ctx context.Context, tenant, dagID, runID string) error
```

DeleteDagRun removes one run \(and, by cascade, its task instances and XCom\). It returns domain.ErrNotFound when no run with that id exists for the DAG, so the API can answer 404 rather than a silent 204 for a bad id.

<a name="Repository.DeleteExpiredCLIDeviceAuthorizations"></a>
### func \(\*Repository\) [DeleteExpiredCLIDeviceAuthorizations](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/cli_device.go#L218>)

```go
func (r *Repository) DeleteExpiredCLIDeviceAuthorizations(ctx context.Context) (int64, error)
```

DeleteExpiredCLIDeviceAuthorizations forgets the device sign\-ins that expired more than ten minutes ago, with the addresses they were started from, and reports how many. The server runs it every minute \(internal/serverapp\), so no address outlives its sign\-in by much more than ten minutes even when nobody starts another.

<a name="Repository.DeletePool"></a>
### func \(\*Repository\) [DeletePool](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_store.go#L68>)

```go
func (r *Repository) DeletePool(ctx context.Context, tenant, name string) error
```

DeletePool removes a pool. It returns domain.ErrNotFound when none matched and domain.ErrConflict when the target is the implicit default pool — Airflow parity: the fallback pool the gate resolves to must never be deleted.

<a name="Repository.DeleteVariable"></a>
### func \(\*Repository\) [DeleteVariable](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L295>)

```go
func (r *Repository) DeleteVariable(ctx context.Context, tenant, key string) error
```

DeleteVariable removes a variable, returning ErrNotFound when none matched.

<a name="Repository.EnsureIssuerUser"></a>
### func \(\*Repository\) [EnsureIssuerUser](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L550>)

```go
func (r *Repository) EnsureIssuerUser(ctx context.Context, tenant, email, provider, subject string, roles []string) (found *auth.User, ok bool, err error)
```

EnsureIssuerUser makes sure a passwordless user linked to \(provider, subject\) exists in tenant with exactly roles \(\#1283\). A new user is created with them; an existing one keeps its id and gets its roles reconciled. A subject already linked in another tenant is domain.ErrConflict, an unknown tenant domain.ErrNotFound and an unknown role domain.ErrValidation.

<a name="Repository.EnsureTenant"></a>
### func \(\*Repository\) [EnsureTenant](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_tenants.go#L58>)

```go
func (r *Repository) EnsureTenant(ctx context.Context, name, displayName string, defaultPoolSlots int, limits domain.TenantLimitsUpdate) (created bool, err error)
```

EnsureTenant creates the tenant name with the built\-in roles, their permissions and the default pool copied from the "default" tenant, all in one transaction \(\#1283\). It is idempotent: for an existing tenant it fills in anything missing and reports created=false. A positive defaultPoolSlots sizes the tenant's default pool to that many slots, on creation or later; a non\-positive value leaves an existing pool alone and gives a new one the default tenant's size. limits sets the tenant limits it carries and leaves the others as they are \(a new tenant starts with none\).

<a name="Repository.FavoriteDagIDs"></a>
### func \(\*Repository\) [FavoriteDagIDs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L302>)

```go
func (r *Repository) FavoriteDagIDs(ctx context.Context, tenant, userID string) (map[string]bool, error)
```

FavoriteDagIDs returns the set of DAG ids the user has favorited.

<a name="Repository.FindAPITokenByHash"></a>
### func \(\*Repository\) [FindAPITokenByHash](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/api_tokens.go#L132>)

```go
func (r *Repository) FindAPITokenByHash(ctx context.Context, hash string) (*auth.APIToken, error)
```

FindAPITokenByHash returns the token whose secret hashes to hash, revoked or expired included, or auth.ErrAPITokenNotFound. It implements auth.APITokenStore.

<a name="Repository.FindCLIDeviceByUserCode"></a>
### func \(\*Repository\) [FindCLIDeviceByUserCode](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/cli_device.go#L89>)

```go
func (r *Repository) FindCLIDeviceByUserCode(ctx context.Context, userCodeHash string) (auth.DeviceAuthorization, error)
```

FindCLIDeviceByUserCode returns the pending, unexpired device sign\-in whose user code hashes to userCodeHash, or domain.ErrNotFound.

<a name="Repository.FindUserByID"></a>
### func \(\*Repository\) [FindUserByID](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L53>)

```go
func (r *Repository) FindUserByID(ctx context.Context, id string) (found *auth.User, ok bool, err error)
```

FindUserByID reloads a user's current authorization state by id: its tenant, roles, and permissions, plus whether the account is active. It is the per\- request source of truth the authenticator uses on token validation. A subject that is not a valid uuid, or that matches no row, yields auth.ErrUserNotFound \(the trusted in\-process minting path has no backing user\); any other failure is returned as\-is so the caller can fail closed.

<a name="Repository.FindUserByLogin"></a>
### func \(\*Repository\) [FindUserByLogin](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L18>)

```go
func (r *Repository) FindUserByLogin(ctx context.Context, tenant, username string) (found *auth.User, passwordHash string, err error)
```

FindUserByLogin loads a user and its bcrypt hash for authentication.

<a name="Repository.FindUserByOIDCSubject"></a>
### func \(\*Repository\) [FindUserByOIDCSubject](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L88>)

```go
func (r *Repository) FindUserByOIDCSubject(ctx context.Context, provider, subject string) (found *auth.User, ok bool, err error)
```

FindUserByOIDCSubject resolves an OIDC identity to a Dexaflow user by its immutable \(provider, subject\) pair, the trusted link key for a returning SSO login. Like FindUserByID it loads the current tenant, roles, and permissions plus the active flag, so the caller reconstructs the same principal the credential path would. A pair matching no row yields auth.ErrUserNotFound \(the signal to consider just\-in\-time provisioning\); any other failure is returned as\-is so the caller can fail closed.

<a name="Repository.GetConnection"></a>
### func \(\*Repository\) [GetConnection](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L409>)

```go
func (r *Repository) GetConnection(ctx context.Context, tenant, connID string) (domain.Connection, error)
```

GetConnection returns a connection with extra decrypted; the password is not returned \(write\-only\). Returns ErrNotFound when absent.

<a name="Repository.GetCurrentSpec"></a>
### func \(\*Repository\) [GetCurrentSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L72>)

```go
func (r *Repository) GetCurrentSpec(ctx context.Context, tenant, dagID string) (domain.DAGSpec, error)
```

GetCurrentSpec returns the parsed spec of the DAG's current version, or domain.ErrNotFound if the DAG or its current version does not exist.

<a name="Repository.GetDag"></a>
### func \(\*Repository\) [GetDag](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L35>)

```go
func (r *Repository) GetDag(ctx context.Context, tenant, dagID string) (domain.DAG, error)
```

GetDag returns a single DAG by its user\-facing id.

<a name="Repository.GetDagRun"></a>
### func \(\*Repository\) [GetDagRun](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L146>)

```go
func (r *Repository) GetDagRun(ctx context.Context, tenant, dagID, runID string) (domain.DagRun, error)
```

GetDagRun returns a single run by its run id.

<a name="Repository.GetDagVersionSpec"></a>
### func \(\*Repository\) [GetDagVersionSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/dag_version_spec.go#L18>)

```go
func (r *Repository) GetDagVersionSpec(ctx context.Context, tenant, dagID string, versionNumber int) (domain.DagVersion, domain.DAGSpec, error)
```

GetDagVersionSpec returns one version of the DAG and the spec registered with it: the version numbered versionNumber \(the 1\-based version\_number of ListDagVersions\), or the newest version when versionNumber is 0. The newest version is not necessarily the current one: after a rollback the current version is an older one. It returns domain.ErrNotFound when the DAG has no such version in the caller's tenant. The spec is shared with the spec cache and must not be mutated.

<a name="Repository.GetPool"></a>
### func \(\*Repository\) [GetPool](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_store.go#L37>)

```go
func (r *Repository) GetPool(ctx context.Context, tenant, name string) (domain.Pool, error)
```

GetPool returns one pool by name, or domain.ErrNotFound.

<a name="Repository.GetVariable"></a>
### func \(\*Repository\) [GetVariable](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L237>)

```go
func (r *Repository) GetVariable(ctx context.Context, tenant, key string) (domain.Variable, error)
```

GetVariable returns one variable by key, or ErrNotFound.

<a name="Repository.HasCipher"></a>
### func \(\*Repository\) [HasCipher](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/variable_crypto.go#L53>)

```go
func (r *Repository) HasCipher() bool
```

HasCipher reports whether a secret key is configured.

<a name="Repository.HistoricalMetrics"></a>
### func \(\*Repository\) [HistoricalMetrics](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/dashboard_stats.go#L45>)

```go
func (r *Repository) HistoricalMetrics(ctx context.Context, tenant string, since, until time.Time) (domain.HistoricalMetrics, error)
```

HistoricalMetrics returns run\- and task\-instance state counts for runs whose logical date falls within \[since, until\], keyed by Dexaflow state name.

<a name="Repository.InvalidateTenant"></a>
### func \(\*Repository\) [InvalidateTenant](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_tenants.go#L17>)

```go
func (r *Repository) InvalidateTenant(name string)
```

InvalidateTenant evicts a tenant from the tenant ID cache, so the next lookup re\-queries the database. Code that deletes or recreates a tenant behind this process calls it; nothing in this repository deletes a tenant.

<a name="Repository.LatestRunsForDags"></a>
### func \(\*Repository\) [LatestRunsForDags](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L114>)

```go
func (r *Repository) LatestRunsForDags(ctx context.Context, tenant string, dagIDs []string, perDag int) (map[string][]domain.DagRun, error)
```

LatestRunsForDags returns up to perDag most\-recent runs for each named DAG, keyed by dag\_id, in a single windowed query \(no per\-DAG round trips\).

<a name="Repository.ListAPITokens"></a>
### func \(\*Repository\) [ListAPITokens](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/api_tokens.go#L167>)

```go
func (r *Repository) ListAPITokens(ctx context.Context, tenant, ownerID string, limit, offset int) ([]auth.APIToken, int, error)
```

ListAPITokens returns a page of the tenant's unrevoked tokens, newest first, and their total. ownerID limits them to one user; empty lists every owner's.

<a name="Repository.ListAuditLogs"></a>
### func \(\*Repository\) [ListAuditLogs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L258>)

```go
func (r *Repository) ListAuditLogs(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.AuditLogEntry, int, error)
```

ListAuditLogs returns a page of audit\-log entries for the tenant, newest first, optionally filtered to a single DAG \(dagID == "" means no filter\).

<a name="Repository.ListAuditLogsAfter"></a>
### func \(\*Repository\) [ListAuditLogsAfter](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L301>)

```go
func (r *Repository) ListAuditLogsAfter(ctx context.Context, tenant, dagID string, after domain.PageCursor, limit int) ([]domain.AuditLogEntry, int, error)
```

ListAuditLogsAfter returns up to limit of the tenant's audit entries strictly before the cursor, newest first, optionally filtered to one DAG, and the number of entries matching the filter: the cursor's Total when it carries one, else a COUNT. It is the keyset form of ListAuditLogs; the cursor key is the entry id.

<a name="Repository.ListConnections"></a>
### func \(\*Repository\) [ListConnections](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L426>)

```go
func (r *Repository) ListConnections(ctx context.Context, tenant string, limit, offset int) ([]domain.Connection, int, error)
```

ListConnections returns a page of connections \(no passwords\) and the total.

<a name="Repository.ListConnectionsFiltered"></a>
### func \(\*Repository\) [ListConnectionsFiltered](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L431>)

```go
func (r *Repository) ListConnectionsFiltered(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int) (conns []domain.Connection, total int, err error)
```

ListConnectionsFiltered returns a page of connections \(no passwords\) matching patterns and the total.

<a name="Repository.ListDagRuns"></a>
### func \(\*Repository\) [ListDagRuns](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L15>)

```go
func (r *Repository) ListDagRuns(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, int, error)
```

ListDagRuns returns a page of runs for a DAG and the total count.

<a name="Repository.ListDagRunsAfter"></a>
### func \(\*Repository\) [ListDagRunsAfter](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L111>)

```go
func (r *Repository) ListDagRunsAfter(ctx context.Context, tenant, dagID string, states []string, after domain.PageCursor, limit int) ([]domain.DagRun, int, error)
```

ListDagRunsAfter returns up to limit of a DAG's runs strictly before the cursor, newest first, and the number of runs matching states \(all runs when states is empty\): the cursor's Total when it carries one, else a COUNT. It is the keyset form of ListDagRuns: the same order, but a deep page is an index range scan instead of a scan past every skipped row.

<a name="Repository.ListDagRunsPage"></a>
### func \(\*Repository\) [ListDagRunsPage](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L84>)

```go
func (r *Repository) ListDagRunsPage(ctx context.Context, tenant, dagID string, limit, offset int) ([]domain.DagRun, error)
```

ListDagRunsPage is ListDagRuns without the total: it skips the COUNT over every run of the DAG, for callers that never use it \(the grid, the latest run header, the in\-memory run scans\).

<a name="Repository.ListDagRunsWithFilter"></a>
### func \(\*Repository\) [ListDagRunsWithFilter](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L33>)

```go
func (r *Repository) ListDagRunsWithFilter(ctx context.Context, tenant, dagID string, filter domain.DagRunFilter) (runs []domain.DagRun, total int, err error)
```

ListDagRunsWithFilter returns a page of runs for a DAG matching the given filter criteria, and the matching total count.

<a name="Repository.ListDagTags"></a>
### func \(\*Repository\) [ListDagTags](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L259>)

```go
func (r *Repository) ListDagTags(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int) (tags []string, total int, err error)
```

ListDagTags returns a page of the tenant's distinct DAG tags, narrowed by an optional pattern \(anywhere in the name\) and prefix pattern, and the total.

<a name="Repository.ListDagVersions"></a>
### func \(\*Repository\) [ListDagVersions](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L20>)

```go
func (r *Repository) ListDagVersions(ctx context.Context, tenant, dagID string) ([]domain.DagVersion, error)
```

ListDagVersions returns the DAG's versions, newest first, with a 1\-based version\_number the UI uses to query version\-scoped structure.

<a name="Repository.ListDags"></a>
### func \(\*Repository\) [ListDags](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L14>)

```go
func (r *Repository) ListDags(ctx context.Context, tenant string, limit, offset int) ([]domain.DAG, int, error)
```

ListDags returns a page of DAGs for the tenant and the total count.

<a name="Repository.ListDagsFiltered"></a>
### func \(\*Repository\) [ListDagsFiltered](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L171>)

```go
func (r *Repository) ListDagsFiltered(ctx context.Context, tenant, runState string, paused *bool, limit, offset int) ([]domain.DAG, int, error)
```

ListDagsFiltered returns a page of active DAGs for the tenant, optionally filtered by paused state and/or latest\-run state, with the matching total. An empty runState or nil paused disables that filter.

<a name="Repository.ListDagsWithFilter"></a>
### func \(\*Repository\) [ListDagsWithFilter](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L200>)

```go
func (r *Repository) ListDagsWithFilter(ctx context.Context, tenant string, filter domain.DagFilter) ([]domain.DAG, int, error)
```

ListDagsWithFilter returns a page of active DAGs matching the given filter criteria and the total count of matching DAGs.

<a name="Repository.ListImportErrors"></a>
### func \(\*Repository\) [ListImportErrors](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L527>)

```go
func (r *Repository) ListImportErrors(ctx context.Context, tenant string) ([]domain.ImportError, error)
```

ListImportErrors returns the tenant's DAG parse/compile errors, newest first.

<a name="Repository.ListImportErrorsFiltered"></a>
### func \(\*Repository\) [ListImportErrorsFiltered](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L546>)

```go
func (r *Repository) ListImportErrorsFiltered(ctx context.Context, tenant, filename, bundleName string, limit, offset int) (errs []domain.ImportError, total int, err error)
```

ListImportErrorsFiltered returns a page of import errors matching the filter criteria and total count.

<a name="Repository.ListPools"></a>
### func \(\*Repository\) [ListPools](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_store.go#L14>)

```go
func (r *Repository) ListPools(ctx context.Context, tenant string, limit, offset int) ([]domain.Pool, int, error)
```

ListPools returns a page of the tenant's named pools and the total count.

<a name="Repository.ListTaskInstanceAttempts"></a>
### func \(\*Repository\) [ListTaskInstanceAttempts](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L261>)

```go
func (r *Repository) ListTaskInstanceAttempts(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.TaskInstance, error)
```

ListTaskInstanceAttempts returns every attempt for \(run, task\), oldest first, the current task\_instances row UNIONed with all archived task\_instance\_history rows. The UI's /tries endpoint needs this to render one navigable tab per attempt; without history, a cleared task shows only the latest attempt and the user cannot inspect prior failures \(Lima bug \#241\).

<a name="Repository.ListTaskInstances"></a>
### func \(\*Repository\) [ListTaskInstances](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L306>)

```go
func (r *Repository) ListTaskInstances(ctx context.Context, tenant, dagID, runID string, _, _ int) ([]domain.TaskInstance, int, error)
```

ListTaskInstances returns the task instances of a run.

<a name="Repository.ListTaskInstancesWithFilter"></a>
### func \(\*Repository\) [ListTaskInstancesWithFilter](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L327>)

```go
func (r *Repository) ListTaskInstancesWithFilter(ctx context.Context, tenant, dagID, runID string, filter domain.TaskInstanceFilter) (tis []domain.TaskInstance, total int, err error)
```

ListTaskInstancesWithFilter returns the task instances of a run matching the given filter.

<a name="Repository.ListUsers"></a>
### func \(\*Repository\) [ListUsers](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L352>)

```go
func (r *Repository) ListUsers(ctx context.Context, tenant string, limit, offset int) ([]domain.User, int, error)
```

ListUsers returns a page of the tenant's accounts, newest first, each with the full set of role names it holds. It never reads or returns password\_hash, the list must not expose secrets. The second result is the unpaged total, so the caller can render total\_entries independent of the page size.

<a name="Repository.ListVariables"></a>
### func \(\*Repository\) [ListVariables](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L17>)

```go
func (r *Repository) ListVariables(ctx context.Context, tenant string, limit, offset int) ([]domain.Variable, int, error)
```

ListVariables returns a page of variables for the tenant and the total count.

<a name="Repository.ListVariablesFiltered"></a>
### func \(\*Repository\) [ListVariablesFiltered](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L22>)

```go
func (r *Repository) ListVariablesFiltered(ctx context.Context, tenant, pattern, prefixPattern string, limit, offset int) (vars []domain.Variable, total int, err error)
```

ListVariablesFiltered returns a page of variables matching patterns and the total count.

<a name="Repository.LocalPasswordUserExists"></a>
### func \(\*Repository\) [LocalPasswordUserExists](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L172>)

```go
func (r *Repository) LocalPasswordUserExists(ctx context.Context, tenant, email string) (bool, error)
```

LocalPasswordUserExists reports whether an address has a usable LOCAL password login in the tenant: an active user row with a password hash.

It backs the boot\-time check on auth.oidc.break\_glass\_emails. An address on that allowlist with no password row is an escape hatch that does not open: the gate admits it and the credential lookup then fails like a wrong password.

The hash matters, not just the row. A user created by OIDC just\-in\-time provisioning has a NULL password\_hash \(the users\_has\_auth constraint permits it because the OIDC subject is the other half\), so the row can exist while no password can ever verify against it.

<a name="Repository.PollCLIDeviceAuthorization"></a>
### func \(\*Repository\) [PollCLIDeviceAuthorization](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/cli_device.go#L142>)

```go
func (r *Repository) PollCLIDeviceAuthorization(ctx context.Context, deviceCodeHash string) (auth.DevicePoll, error)
```

PollCLIDeviceAuthorization answers one poll of the device sign\-in whose device code hashes to deviceCodeHash, in one transaction that locks its row, so concurrent polls on any replica see one another: an approved sign\-in is redeemed, and deleted, by exactly one poll. A poll sooner than the interval is a slow\_down that lengthens it; a pending one records the poll. A canceled sign\-in answers denied once and is deleted.

<a name="Repository.PoolSlotUsage"></a>
### func \(\*Repository\) [PoolSlotUsage](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_store.go#L94>)

```go
func (r *Repository) PoolSlotUsage(ctx context.Context, tenant string) (map[string]domain.PoolUsage, error)
```

PoolSlotUsage returns per\-pool occupancy for the tenant, keyed by pool name \(a task instance with no pool is counted under the implicit default\_pool\). Each state sums its task instances' pool\_slots, so the slots match what the admission gate charges \(ADR 0066, \#1499\). It feeds the Airflow PoolResponse occupancy fields.

<a name="Repository.ReconcileUserRoles"></a>
### func \(\*Repository\) [ReconcileUserRoles](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L235>)

```go
func (r *Repository) ReconcileUserRoles(ctx context.Context, userID string, roleNames []string) error
```

ReconcileUserRoles makes the user's granted roles exactly roleNames, atomically: it is how the identity provider stays authoritative over an OIDC user's roles. On each OIDC login the caller passes the group\-mapped role set, and this sets the DB user\_roles to precisely that set, so a demotion or deprovisioning at the IdP takes effect on the next login and the per\-request authz reload sees it.

Every name is resolved to a role id in the user's OWN tenant BEFORE any write, so a name that is not a role in that tenant fails closed as domain.ErrValidation with the prior grants untouched, the login path turns that into a rejected, audited login rather than silently wiping the user's roles. The delete and the inserts run in one transaction, making the operation idempotent \(reconciling the same set yields the same rows\) and an empty roleNames a full clear \(default\-deny\).

<a name="Repository.RecordAuthEvent"></a>
### func \(\*Repository\) [RecordAuthEvent](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L143>)

```go
func (r *Repository) RecordAuthEvent(ctx context.Context, tenant, actorUserID, action, email, outcome string, extra map[string]string) error
```

RecordAuthEvent records an authentication event to the audit log \(H5\): OIDC login success/failure, tenant\-pin rejection, JIT provisioning, break\-glass login, and logout. It NEVER records tokens or the client secret, only the actor's email, the resolved tenant \(best\-effort\), the outcome, and small non\-secret detail fields \(e.g. the rejection reason, the attempted tenant claim\). It is best\-effort: the caller logs and continues on error so a flaky audit sink never turns a security decision \(a 403 rejection, a successful login\) into a 5xx.

The event is scoped to the resolved tenant when known; events that never resolved a tenant \(a login failure, a tenant\-pin rejection\) fall back to the "default" tenant so the row still lands, with the attempted values in the metadata. resourceID carries the email so account\-scoped auth activity is filterable alongside user.create.

<a name="Repository.RecordResourceAudit"></a>
### func \(\*Repository\) [RecordResourceAudit](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L102>)

```go
func (r *Repository) RecordResourceAudit(ctx context.Context, tenant, actorUserID, action, resourceType, resourceID string, metadata map[string]string) error
```

RecordResourceAudit logs an accepted admin write to a named resource of the tenant, such as variable.update or pool.delete \(\#2228\), with the acting user as the owner, scoped to resourceType and resourceID. metadata holds the names of the fields the request set and other non\-secret detail; the caller never passes a secret value. The resource's name is also written to the metadata under resourceType, since the EventLog shape of the API has no field for a resource other than a DAG.

<a name="Repository.RecordRunActionAudit"></a>
### func \(\*Repository\) [RecordRunActionAudit](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L31>)

```go
func (r *Repository) RecordRunActionAudit(ctx context.Context, tenant, userID, action, dagID, runID string, metadata map[string]string) error
```

RecordRunActionAudit logs a run\-level action on the DAG's Audit Log tab, as RecordTaskActionAudit does, with extra metadata fields next to run\_id \(for example requested\_state when a PATCH stored another state, \#1766\).

<a name="Repository.RecordSecretLivenessDenial"></a>
### func \(\*Repository\) [RecordSecretLivenessDenial](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L229>)

```go
func (r *Repository) RecordSecretLivenessDenial(ctx context.Context, tenantID, dagID, runID, taskID string, tryNumber int, kind, mode string) error
```

RecordSecretLivenessDenial records that the secret\-path liveness gate fired for a task instance whose attempt is no longer live \(ADR 0055\): a would\-have\-denied in observe mode, or a denial in enforce mode. It is scoped to the DAG resource so the event surfaces on the DAG's Audit Log tab, with the kind \("variables" or "connections"\), the run, task, attempt, and the gate mode in metadata. It records identity \+ kind \+ mode only, never secret names or values.

tenantID is the tenant UUID the agent token carries \(not the tenant name\): the caller is the agent RPC path, which passes AgentIdentity.TenantID. Resolving it by name silently dropped every row \(\#722\), including enforce\-mode security denials, so mirror the secret\-delivery path and parse it as a UUID.

<a name="Repository.RecordSecretScopeWarning"></a>
### func \(\*Repository\) [RecordSecretScopeWarning](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L196>)

```go
func (r *Repository) RecordSecretScopeWarning(ctx context.Context, tenantID, dagID, runID, taskID, kind string, declared, total int) error
```

RecordSecretScopeWarning records that a task received the full tenant secret set while it declared only a narrower subset, possibly an empty one \(\#800\) \(ADR 0045, ADR 0055\): under secret\_scoping: enforce it would receive only its declared set. It is scoped to the DAG resource so the event surfaces on the DAG's Audit Log tab, with the kind \("variables" or "connections"\), the run and task, and the declared/total counts in metadata. It records counts only, never secret names or values.

tenantID is the tenant UUID the agent token carries \(not the tenant name\): the caller is the agent RPC path, which passes AgentIdentity.TenantID. Resolving it by name silently dropped every row \(\#722\), so mirror the secret\-delivery path and parse it as a UUID.

<a name="Repository.RecordTaskActionAudit"></a>
### func \(\*Repository\) [RecordTaskActionAudit](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L19>)

```go
func (r *Repository) RecordTaskActionAudit(ctx context.Context, tenant, userID, action, dagID, runID, taskID string, tryNumber int) error
```

RecordTaskActionAudit logs a task\-level action \(clear, mark state\) with the acting user and the run/task/try in metadata, so the Audit Log view shows the owner and the task columns. Scoped to the DAG \(resource\_id = dag\_id\) so it appears on the DAG's Audit Log tab.

<a name="Repository.RecordUserCreatedAudit"></a>
### func \(\*Repository\) [RecordUserCreatedAudit](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_audit.go#L69>)

```go
func (r *Repository) RecordUserCreatedAudit(ctx context.Context, tenant, actorUserID, createdUserID, email, roles string) error
```

RecordUserCreatedAudit logs an account creation with the acting admin as the owner and the new account's email and granted roles in metadata, scoped to the "user" resource so account\-management actions are visible in the Audit Log. The roles arrive as a single comma\-joined string \(empty when none were granted\).

<a name="Repository.ReencryptSecrets"></a>
### func \(\*Repository\) [ReencryptSecrets](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/reencrypt.go#L47>)

```go
func (r *Repository) ReencryptSecrets(ctx context.Context) (ReencryptResult, error)
```

ReencryptSecrets rewrites every stored connection secret, and every sealed variable value \(\#507\), that a non\-primary key opened, so the rotation can finish and the old key be retired.

Without this a rotation never completes: the previous key stays in the read set forever. "Set the new key and keep the old one" is not a rotation, it is a second key.

This is the Pro boot sweep of ADR 0019. Lite switches it off and migrates only through \`dexaflow lite migrate\-key\` \(ADR 0065\), which runs in one verified transaction instead.

A no\-op when nothing is stale, so it is safe to run on every boot: with a single key every row reports fresh and nothing is written.

A row no configured key can open is LEFT ALONE and reported, both in the result and as an error. Its ciphertext is the only copy of a credential, the operator may still find the key, and overwriting or deleting it would destroy what it protects. Rows that could be migrated are still migrated. A row the optimistic guard skipped is counted in the result: it was not moved by this pass, so the pass is not complete, whatever else it did.

<a name="Repository.RegisterDagVersion"></a>
### func \(\*Repository\) [RegisterDagVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L107>)

```go
func (r *Repository) RegisterDagVersion(ctx context.Context, tenant string, spec domain.DAGSpec, specHash string) (bool, error)
```

RegisterDagVersion inserts a version of the DAG keyed by specHash and makes it current, creating the DAG on its first registration. It is idempotent: an existing hash yields created=false and writes nothing, even when that hash is an older version than the current one.

Everything runs in one transaction that holds the DAG's row lock, so registers of one DAG are serialized and a refused or failed register leaves no partial state \(\#1702\). The dags row's metadata \(schedule, catchup, start\_date, max\_active\_runs, description, owner, tags\) is rewritten only when the new version becomes current, so it always describes the current version. is\_paused is set only when this register creates the DAG \(pausedOnCreation\); a new version of an existing DAG never changes it.

<a name="Repository.RegisterDagVersionAs"></a>
### func \(\*Repository\) [RegisterDagVersionAs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L126>)

```go
func (r *Repository) RegisterDagVersionAs(ctx context.Context, tenant string, reg domain.DagRegistration) (domain.DagRegistered, error)
```

RegisterDagVersionAs is RegisterDagVersionBy with an optional is\_paused \(reg.IsPaused\): when set, the DAG ends the register in that state, whether the register creates it, adds a version or finds the content already registered. A change to an existing DAG is audited as dag.pause or dag.unpause in the same transaction, and the register's own audit entry records the requested state. It reports whether a version was created and the DAG's is\_paused afterwards.

<a name="Repository.RegisterDagVersionBy"></a>
### func \(\*Repository\) [RegisterDagVersionBy](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L114>)

```go
func (r *Repository) RegisterDagVersionBy(ctx context.Context, tenant, actorUserID string, spec domain.DAGSpec, specHash string) (bool, error)
```

RegisterDagVersionBy is RegisterDagVersion on behalf of the user actorUserID: the version's created\_by and its audit entry name that user. An actor that is not a user id \(empty, or Lite's dev principal\) leaves both without a user.

<a name="Repository.RemoveFavorite"></a>
### func \(\*Repository\) [RemoveFavorite](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L294>)

```go
func (r *Repository) RemoveFavorite(ctx context.Context, tenant, userID, dagID string) error
```

RemoveFavorite clears a DAG's favorite mark for the user \(idempotent\).

<a name="Repository.ResumeTenant"></a>
### func \(\*Repository\) [ResumeTenant](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/tenant_limits.go#L93>)

```go
func (r *Repository) ResumeTenant(ctx context.Context, tenant string) error
```

ResumeTenant lifts the tenant's suspension. Resuming a tenant that is not suspended does nothing; an unknown tenant is ErrNotFound.

<a name="Repository.RevokeAPIToken"></a>
### func \(\*Repository\) [RevokeAPIToken](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/api_tokens.go#L201>)

```go
func (r *Repository) RevokeAPIToken(ctx context.Context, tenant, id, ownerID string) (auth.APIToken, error)
```

RevokeAPIToken revokes the tenant's live token id and returns it with its owner. ownerID limits the revoke to that user's tokens; empty allows any owner's. A token that is not found under those limits, or is already revoked, is domain.ErrNotFound.

<a name="Repository.RoleExists"></a>
### func \(\*Repository\) [RoleExists](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L208>)

```go
func (r *Repository) RoleExists(ctx context.Context, tenant, role string) (bool, error)
```

RoleExists reports whether a role name exists for the tenant. The OIDC login path uses it to fail closed on a misconfigured default\_role before minting a token for a returning user \(the JIT path validates roles inside CreateOIDCUser\).

<a name="Repository.RolePermissions"></a>
### func \(\*Repository\) [RolePermissions](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L574>)

```go
func (r *Repository) RolePermissions(ctx context.Context, tenant, role string) ([]auth.Permission, error)
```

RolePermissions returns the grants of the role called role in tenant, for a CI OIDC session that acts with that role alone \(ADR 0092\). A tenant without such a role, or no such tenant, is auth.ErrRoleNotFound.

<a name="Repository.RunsKeepingVersion"></a>
### func \(\*Repository\) [RunsKeepingVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/clear_rebind.go#L26>)

```go
func (r *Repository) RunsKeepingVersion(ctx context.Context, tenant, dagID string, runIDs []string, opts domain.ClearOptions) ([]domain.ClearVersionKept, error)
```

RunsKeepingVersion lists the runs, among runIDs, that a clear with opts asks to move to the DAG's current version and that keep their own, each with the reason \(ADR 0086\). It writes nothing. A run that does not exist in the tenant is domain.ErrNotFound, so the API refuses an unknown run before it previews or clears any. The list is empty when the clear does not re\-bind \(no run\_on\_latest\_version, or no reset\_dag\_runs\) and for a run already on the current version.

The answer is read before the clear. ClearTaskInstances decides again inside its transaction, so a run that finishes in between moves although it is listed here, and a run another clear re\-opens in between keeps its version although it is not.

<a name="Repository.SecretConnectionURIs"></a>
### func \(\*Repository\) [SecretConnectionURIs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L76>)

```go
func (r *Repository) SecretConnectionURIs(ctx context.Context, tenantID string) (map[string]string, error)
```

SecretConnectionURIs returns the tenant's connections as conn\_id→Airflow URI \(password decrypted\), for delivering to task pods \(ADR 0021\). The agent exports them as AIRFLOW\_CONN\_\<CONN\_ID\>. tenantID is the tenant UUID carried by the agent token. Never expose these in UI/API responses.

<a name="Repository.SecretConnectionURIsScoped"></a>
### func \(\*Repository\) [SecretConnectionURIsScoped](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L144>)

```go
func (r *Repository) SecretConnectionURIsScoped(ctx context.Context, tenantID string, names []string) (map[string]string, error)
```

SecretConnectionURIsScoped returns only the named subset of the tenant's connections as Airflow URIs \(password decrypted\), filtered in the query \(ADR 0055 D1\). It backs secret\_scoping: enforce. An empty name set returns nothing without a query. Never expose these in UI/API responses. It shares the per\-connection decrypt\-and\-skip\-on\-failure semantics of SecretConnectionURIs: one undecryptable connection is skipped with a warning, never blinding the rest of the declared set.

<a name="Repository.SecretVariables"></a>
### func \(\*Repository\) [SecretVariables](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L55>)

```go
func (r *Repository) SecretVariables(ctx context.Context, tenantID string) (map[string]string, error)
```

SecretVariables returns the tenant's variables as key→value, for delivering to task pods \(ADR 0021\). The agent exports them as AIRFLOW\_VAR\_\<KEY\>. tenantID is the tenant UUID carried by the agent token \(not the tenant name\).

<a name="Repository.SecretVariablesScoped"></a>
### func \(\*Repository\) [SecretVariablesScoped](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L118>)

```go
func (r *Repository) SecretVariablesScoped(ctx context.Context, tenantID string, names []string) (map[string]string, error)
```

SecretVariablesScoped returns only the named subset of the tenant's variables, filtered in the query \(ADR 0055 D1: scope in the SQL, never post\-filter the decrypted whole vault in the handler\). It backs secret\_scoping: enforce, where a task receives only the Variables it declared. An empty name set returns nothing without a query, enforce's load\-bearing \[\] case. tenantID is the tenant UUID carried by the agent token.

<a name="Repository.SetConnection"></a>
### func \(\*Repository\) [SetConnection](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L325>)

```go
func (r *Repository) SetConnection(ctx context.Context, tenant string, c domain.Connection) error
```

SetConnection creates or updates a connection, encrypting password and extra at rest. It fails if no encryption cipher is configured \(never stores a credential in plaintext, ADR 0019\).

<a name="Repository.SetConnectionPatch"></a>
### func \(\*Repository\) [SetConnectionPatch](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L381>)

```go
func (r *Repository) SetConnectionPatch(ctx context.Context, tenant string, p domain.ConnectionPatch) error
```

SetConnectionPatch upserts a connection with tri\-state field semantics \(\#887\): a nil field is preserved \(NULL param → COALESCE keeps the stored value\), a non\-nil field is written exactly \("" clears it\). Password and extra are encrypted when set. It is how the API write path distinguishes an omitted field from an explicit empty one without reintroducing the password\-wipe, and where a masked secret round\-trip is preserved \(the handler maps a masked password to a nil field, and unmasks extra, before calling here\). It fails if no encryption cipher is configured \(never stores a credential in plaintext, ADR 0019\).

<a name="Repository.SetCurrentDagVersion"></a>
### func \(\*Repository\) [SetCurrentDagVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/set_current_version.go#L40>)

```go
func (r *Repository) SetCurrentDagVersion(ctx context.Context, tenant, dagID string, sel domain.DagVersionSelector, actorUserID string, admit func(domain.DAGSpec) error) (domain.CurrentVersionChange, error)
```

SetCurrentDagVersion makes a registered version of dagID its current version again: the rollback \(or roll forward\) of \#1702. New runs are created with that version from the next scheduler tick; runs that already exist keep the version they were created with \(dag\_runs.dag\_version\_id is never rewritten\).

The target spec must first pass what a register of it would have to pass today: its declared Variables and Connections, the tenant's limits, and admit \(the caller's checks, such as the execution policy\); a refusal is returned as is. Like RegisterDagVersion, these checks run before the transaction, on reads of their own, so the transaction never waits for a second connection while it holds one and the DAG's row lock.

The write runs in one transaction under the same row lock as RegisterDagVersion, so it is serialized with registers of the DAG. Under the lock the target is resolved again and must be the version that was checked \(else domain.ErrConflict\); then the target version's metadata \(schedule, catchup, start\_date, max\_active\_runs, description, owner, tags\) is applied to the dags row together with current\_version\_id. is\_paused is never changed: pausing is an operator decision, not a deploy one.

A target that is already current changes nothing and records nothing \(Changed is false\). An unknown DAG, an unknown version and a DAG of another tenant are domain.ErrNotFound. The change is audited as dag.version.set\_current by actorUserID, with the from and to versions.

<a name="Repository.SetCurrentDagVersionAs"></a>
### func \(\*Repository\) [SetCurrentDagVersionAs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/set_current_version.go#L47>)

```go
func (r *Repository) SetCurrentDagVersionAs(ctx context.Context, tenant string, req domain.DagSetCurrent) (domain.CurrentVersionChange, error)
```

SetCurrentDagVersionAs is SetCurrentDagVersion for req, whose RequireDigest, while warn or enforce, the audit entry records with whether the target's image is pinned by digest \(\#1703\).

<a name="Repository.SetDagRunState"></a>
### func \(\*Repository\) [SetDagRunState](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L567>)

```go
func (r *Repository) SetDagRunState(ctx context.Context, tenant, dagID, runID, state string) error
```

SetDagRunState sets a DAG run's state directly, backing the UI's mark run success/failed actions. Terminal states stamp ended\_at; re\-opening to a non\-terminal state clears it. started\_at is preserved. A terminal state is a verdict on the run's work too: in the same transaction its unfinished task instances are settled as Airflow 3 does, so a running attempt is told to stop and no task is left waiting forever \(\#2425\).

<a name="Repository.SetImportError"></a>
### func \(\*Repository\) [SetImportError](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_registration.go#L579>)

```go
func (r *Repository) SetImportError(ctx context.Context, tenant string, e domain.ImportError) error
```

SetImportError records \(or replaces\) the parse/compile error for a file.

<a name="Repository.SetPaused"></a>
### func \(\*Repository\) [SetPaused](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L48>)

```go
func (r *Repository) SetPaused(ctx context.Context, tenant, dagID string, paused bool) (domain.DAG, error)
```

SetPaused toggles the paused flag of a DAG.

<a name="Repository.SetPausedBy"></a>
### func \(\*Repository\) [SetPausedBy](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_dags.go#L55>)

```go
func (r *Repository) SetPausedBy(ctx context.Context, tenant, actorUserID, dagID string, paused bool) (domain.DAG, error)
```

SetPausedBy is SetPaused on behalf of the user actorUserID: a change of is\_paused is audited as dag.pause or dag.unpause in the same transaction; a request for the state the DAG is already in records nothing.

<a name="Repository.SetPool"></a>
### func \(\*Repository\) [SetPool](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/pool_store.go#L54>)

```go
func (r *Repository) SetPool(ctx context.Context, tenant string, p domain.Pool) error
```

SetPool creates or updates a pool \(its slot cap and description\). The is\_default flag is not writable through this path: only the seed migration marks the implicit default pool.

<a name="Repository.SetTaskInstanceState"></a>
### func \(\*Repository\) [SetTaskInstanceState](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L615>)

```go
func (r *Repository) SetTaskInstanceState(ctx context.Context, tenant, dagID, runID, taskID, state string) error
```

SetTaskInstanceState sets a task instance's state directly, backing the UI's "mark success"/"mark failed" actions. It does not run the task. A user's state is a verdict, so it clears an infra failure kind and confirms the row: a reaped task marked failed is neither re\-placed nor overridden by a late SUCCESS record \(ADR 0052 amendment\).

<a name="Repository.SetUserActive"></a>
### func \(\*Repository\) [SetUserActive](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L387>)

```go
func (r *Repository) SetUserActive(ctx context.Context, tenant, actorUserID, userID string, active bool) (domain.User, error)
```

SetUserActive activates or deactivates one user of the tenant and records user.activate or user.deactivate in the audit log, in one transaction, so the change and its record land together \(\#960\). actorUserID is the admin acting. An unknown user, a malformed id or a user of another tenant is domain.ErrNotFound. The per\-request authz reload reads the flag, so a deactivated user's sessions and API tokens stop working at their next request. Deactivating also ends them for good: the user's live API tokens are revoked and its session cutoff is stamped in the same transaction, so a later reactivation does not bring back credentials issued before it.

<a name="Repository.SetUserPassword"></a>
### func \(\*Repository\) [SetUserPassword](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_users.go#L449>)

```go
func (r *Repository) SetUserPassword(ctx context.Context, tenant, email, hash string) (bool, error)
```

SetUserPassword sets a user's bcrypt hash by email, returning whether a user was updated \(false when no such user exists\). Used by \`dexaflow lite reset\-password\`.

<a name="Repository.SetVariable"></a>
### func \(\*Repository\) [SetVariable](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L253>)

```go
func (r *Repository) SetVariable(ctx context.Context, tenant string, v domain.Variable) error
```

SetVariable creates or updates a variable, always overwriting the value and preserving description when empty. It is the full\-write convenience the storage tests and internal callers use; the tri\-state API write path goes through SetVariablePatch.

<a name="Repository.SetVariablePatch"></a>
### func \(\*Repository\) [SetVariablePatch](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_secrets.go#L275>)

```go
func (r *Repository) SetVariablePatch(ctx context.Context, tenant string, p domain.VariablePatch) error
```

SetVariablePatch upserts a variable with tri\-state semantics on description \(\#887\): a nil Description is preserved \(NULL param → COALESCE\), a non\-nil one is written \("" clears it\). The \`value\` column is NOT NULL, so value cannot be preserved at the SQL layer \(a NULL param is rejected on the INSERT arm before COALESCE\); the API handler therefore resolves an omitted/masked value to the stored value before calling here, and Value is expected non\-nil \(a nil Value defensively clears to empty rather than panicking\).

<a name="Repository.SuspendTenant"></a>
### func \(\*Repository\) [SuspendTenant](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/tenant_limits.go#L72>)

```go
func (r *Repository) SuspendTenant(ctx context.Context, tenant string, s domain.TenantSuspension) error
```

SuspendTenant suspends the tenant, or changes the mode or reason of its suspension \(\#2181\). From then on no new run is created for it, manual or scheduled; its DAGs keep their own paused flag. Under stop it also fails the tenant's queued and running task instances; the scheduler repeats that on every tick, so an attempt dispatched while this call ran is stopped too. An unknown tenant is ErrNotFound and a suspension the engine cannot apply is ErrValidation.

<a name="Repository.TaskInstancesForRuns"></a>
### func \(\*Repository\) [TaskInstancesForRuns](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_runs.go#L634>)

```go
func (r *Repository) TaskInstancesForRuns(ctx context.Context, tenant, dagID string, runIDs []string) ([]domain.TaskInstance, error)
```

TaskInstancesForRuns returns the task instances of the given runs of a DAG in one query, ordered by run\_id, task\_id, try\_number, for the grid summaries.

<a name="Repository.TenantExists"></a>
### func \(\*Repository\) [TenantExists](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_tenants.go#L30>)

```go
func (r *Repository) TenantExists(ctx context.Context, name string) (bool, error)
```

TenantExists reports whether a tenant with this name exists.

It backs the boot\-time check on auth.oidc.tenant\_claims: a claim value mapped to a tenant that does not exist denies every login carrying it, and since the only tenant anything creates is "default" \(migration 001\), a typo there is unrecoverable without direct SQL. A lookup failure is returned rather than folded into false, so the caller can tell "absent" from "could not ask".

<a name="Repository.TenantHasDefaultPool"></a>
### func \(\*Repository\) [TenantHasDefaultPool](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_tenants.go#L120>)

```go
func (r *Repository) TenantHasDefaultPool(ctx context.Context, tenant string) (bool, error)
```

TenantHasDefaultPool reports whether the tenant has its default pool.

<a name="Repository.TenantLimits"></a>
### func \(\*Repository\) [TenantLimits](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/tenant_limits.go#L18>)

```go
func (r *Repository) TenantLimits(ctx context.Context, tenant string) (domain.TenantLimits, error)
```

TenantLimits returns the limits the operator set for tenant; a zero field is unlimited.

<a name="Repository.TenantRolePermissions"></a>
### func \(\*Repository\) [TenantRolePermissions](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_tenants.go#L115>)

```go
func (r *Repository) TenantRolePermissions(ctx context.Context, tenant string) ([]string, error)
```

TenantRolePermissions lists a tenant's built\-in role grants as "role:action:resource", so a caller can compare two tenants' ladders.

<a name="Repository.TenantSuspension"></a>
### func \(\*Repository\) [TenantSuspension](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/tenant_limits.go#L56>)

```go
func (r *Repository) TenantSuspension(ctx context.Context, tenant string) (domain.TenantSuspension, error)
```

TenantSuspension returns the tenant's suspension; the zero value means it is not suspended.

<a name="Repository.TenantUUID"></a>
### func \(\*Repository\) [TenantUUID](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repo_tenants.go#L42>)

```go
func (r *Repository) TenantUUID(ctx context.Context, name string) (string, error)
```

TenantUUID resolves a tenant name to its UUID string, the form the agent token carries and that the secret\-delivery methods expect.

<a name="Repository.TouchAPIToken"></a>
### func \(\*Repository\) [TouchAPIToken](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/api_tokens.go#L152>)

```go
func (r *Repository) TouchAPIToken(ctx context.Context, id string, at time.Time) (bool, error)
```

TouchAPIToken records that the token was used at at, unless its last use is less than five minutes before at, and reports whether it wrote. The throttle is the UPDATE's own predicate, so concurrent requests write once, and an older time never moves the last use back. It implements auth.APITokenStore.

<a name="RetentionStore"></a>
## type [RetentionStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L17-L25>)

RetentionStore is the sqlc\-backed implementation of retention.Store.

```go
type RetentionStore struct {
    // contains filtered or unexported fields
}
```

<a name="NewRetentionStore"></a>
### func [NewRetentionStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L35>)

```go
func NewRetentionStore(pg *Postgres) *RetentionStore
```

NewRetentionStore builds a RetentionStore over the given Postgres connection.

<a name="RetentionStore.CountEligible"></a>
### func \(\*RetentionStore\) [CountEligible](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L248>)

```go
func (s *RetentionStore) CountEligible(ctx context.Context, runCutoff, auditCutoff, usageCutoff *time.Time) (retention.Counts, error)
```

CountEligible counts what a retention cycle would delete, for the dry run: expired settled runs and their task instances when runCutoff is set, audit rows when auditCutoff is set \(system rows included\), usage events when usageCutoff is set. Each tenant is counted through its own index range and stops at the dry\-run cap, so the count's cost is bounded per tenant; Capped reports that some tenant reached it and the totals are a lower bound.

<a name="RetentionStore.CountEligibleForTenant"></a>
### func \(\*RetentionStore\) [CountEligibleForTenant](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L286>)

```go
func (s *RetentionStore) CountEligibleForTenant(ctx context.Context, tenant string, runCutoff, auditCutoff *time.Time) (retention.Counts, error)
```

CountEligibleForTenant is CountEligible for one tenant, each class stopped at the dry\-run cap.

<a name="RetentionStore.DeleteAuditLog"></a>
### func \(\*RetentionStore\) [DeleteAuditLog](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L188>)

```go
func (s *RetentionStore) DeleteAuditLog(ctx context.Context, tenant string, cutoff time.Time, limit int) (int64, error)
```

DeleteAuditLog removes up to limit of the tenant's audit rows older than cutoff, oldest first. An empty tenant means the tenant\-less system rows.

<a name="RetentionStore.DeleteFinishedRuns"></a>
### func \(\*RetentionStore\) [DeleteFinishedRuns](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L60>)

```go
func (s *RetentionStore) DeleteFinishedRuns(ctx context.Context, tenant string, cutoff time.Time, maxRuns, rowLimit int) (retention.Counts, error)
```

DeleteFinishedRuns removes, in one transaction, at most rowLimit rows of up to maxRuns of the tenant's settled runs that ended before cutoff, run rows included. The runs are locked first \(which re\-checks that they are still eligible\), then their rows go children first: attempt history, XCom index, task instances, and last the runs whose children are all gone. When the limit runs out inside a run, the transaction commits what it removed and the next call picks the run up again, so no transaction grows with the size of a DAG. The FK cascades find nothing left to do.

<a name="RetentionStore.DeleteUsageEvents"></a>
### func \(\*RetentionStore\) [DeleteUsageEvents](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L231>)

```go
func (s *RetentionStore) DeleteUsageEvents(ctx context.Context, cutoff time.Time, limit int) (int64, error)
```

DeleteUsageEvents removes up to limit usage events older than cutoff, a prefix of the order the service API serves them, and moves the pruned mark to the last one in the same statement \(see DeleteExpiredUsageEvents\).

<a name="RetentionStore.RecordRetentionPurge"></a>
### func \(\*RetentionStore\) [RecordRetentionPurge](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L212>)

```go
func (s *RetentionStore) RecordRetentionPurge(ctx context.Context, tenant string, rows int64, cutoff time.Time) error
```

RecordRetentionPurge writes an audit entry recording that a retention cycle deleted rows audit rows older than cutoff in the tenant's scope; an empty tenant is the system rows.

<a name="RetentionStore.TenantIDs"></a>
### func \(\*RetentionStore\) [TenantIDs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/retention_store.go#L40>)

```go
func (s *RetentionStore) TenantIDs(ctx context.Context) ([]string, error)
```

TenantIDs lists every tenant.

<a name="RowQuerier"></a>
## type [RowQuerier](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L69-L71>)

RowQuerier runs a query. \*pgx.Conn, pgx.Tx and \*pgxpool.Pool satisfy it.

```go
type RowQuerier interface {
    Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
```

<a name="RowQueryer"></a>
## type [RowQueryer](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keylock.go#L23-L25>)

RowQueryer runs a single\-row query. \*pgx.Conn and \*pgxpool.Pool satisfy it.

```go
type RowQueryer interface {
    QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
```

<a name="RowSecurityStatus"></a>
## type [RowSecurityStatus](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/tenant_binding.go#L69-L74>)

RowSecurityStatus reports, per tenant table, whether row level security is enabled and forced, and whether its tenant policy exists.

```go
type RowSecurityStatus struct {
    Table   string
    Enabled bool
    Forced  bool
    Policy  bool
}
```

<a name="ReadRowSecurity"></a>
### func [ReadRowSecurity](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/tenant_binding.go#L87>)

```go
func ReadRowSecurity(ctx context.Context, db queryer) ([]RowSecurityStatus, error)
```

ReadRowSecurity reads the row level security state of the tenant tables.

<a name="RowSecurityStatus.On"></a>
### func \(RowSecurityStatus\) [On](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/tenant_binding.go#L79>)

```go
func (s RowSecurityStatus) On() bool
```

On reports whether the table is protected: enabled, forced \(so the table owner, which is usually the server's own role, is held to it too\) and with its policy.

<a name="SchedulerStore"></a>
## type [SchedulerStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L24-L48>)

SchedulerStore is the sqlc\-backed implementation of scheduler.Store.

```go
type SchedulerStore struct {
    // contains filtered or unexported fields
}
```

<a name="NewSchedulerStore"></a>
### func [NewSchedulerStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L58>)

```go
func NewSchedulerStore(pg *Postgres) *SchedulerStore
```

NewSchedulerStore builds a SchedulerStore over the given Postgres connection.

<a name="SchedulerStore.ActiveRuns"></a>
### func \(\*SchedulerStore\) [ActiveRuns](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L148>)

```go
func (s *SchedulerStore) ActiveRuns(ctx context.Context) ([]scheduler.RunState, error)
```

ActiveRuns loads every active dag run and projects it into the scheduler's RunState \(topology \+ per\-task state\), the read side of a scheduler tick. A run of a paused DAG is included and marked Held \(\#1705\).

<a name="SchedulerStore.ActiveWarmTargets"></a>
### func \(\*SchedulerStore\) [ActiveWarmTargets](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store_warm.go#L128>)

```go
func (s *SchedulerStore) ActiveWarmTargets(ctx context.Context) ([]executor.WarmTarget, error)
```

ActiveWarmTargets returns one warm target per dag\_version the warm pool keeps \(ADR 0058 N1b2b, ADR 0082\), the read side of a warm\-pool reconcile tick: the current version of every active, unpaused DAG that declares min\_idle\_workers, prewarmed before its first run \(\#1707\), and the version of every active run that is still its DAG's current version. A run whose version a register, a rollback or a promote superseded keeps running on it, but the version is no longer a target, so its pool drains: idle workers are deleted and busy ones finish their attempt first, and the run's later attempts take dedicated pods. A run of a DAG with no current version keeps its version's target, as before. It reuses the same cached\-spec path as ActiveRuns: the spec is immutable per dag\_version\_id, so N active runs sharing a version decode it once, and the effective target is derived from the DAG author's min\_idle\_workers under the operator's clamp/fallback. It implements executor.WarmTargetSource without the executor importing storage. A version none of whose tasks dispatch would place on a warm worker gets no target \(\#2065\). A run held by its paused DAG \(\#1705\) keeps a target only while one of its tasks is live, since nothing else of it is dispatched until the unpause, and a paused DAG is not prewarmed.

<a name="SchedulerStore.ApplyTransition"></a>
### func \(\*SchedulerStore\) [ApplyTransition](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L437>)

```go
func (s *SchedulerStore) ApplyTransition(ctx context.Context, runID, taskID string, to domain.TaskState) error
```

ApplyTransition moves a task instance to a new state.

<a name="SchedulerStore.ApplyTransitions"></a>
### func \(\*SchedulerStore\) [ApplyTransitions](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L457>)

```go
func (s *SchedulerStore) ApplyTransitions(ctx context.Context, runID string, taskIDs []string, from, to domain.TaskState) ([]string, error)
```

ApplyTransitions moves every listed task of a run that is still in the from state to the SAME target state in one UPDATE, the batched form of the plain per\-task transition. The scheduler groups a tick's plain state\-set transitions by source and target state and flushes each group here, collapsing R updates into one per group. The from guard is the state the tick's snapshot read: a row a concurrent writer moved since then is left alone \(\#1605\). It returns the task ids that actually moved. An empty list is a no\-op.

<a name="SchedulerStore.ClaimAlertAttempt"></a>
### func \(\*SchedulerStore\) [ClaimAlertAttempt](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L746>)

```go
func (s *SchedulerStore) ClaimAlertAttempt(ctx context.Context, runID string, maxAttempts int, backoff time.Duration) (int, error)
```

ClaimAlertAttempt atomically claims one on\-failure send attempt for a run \(\#431\). The UPDATE consumes an attempt and sets the next\-attempt time, but only while the episode is undelivered, within budget, and past its backoff — see the query for why each predicate exists. pgx.ErrNoRows means the claim was refused on one of those grounds: not an error, just a lost claim, so report won=false.

Claiming an attempt is NOT the same as recording delivery; that is MarkRunAlertDelivered. Conflating the two is what made a failed send a permanently lost page. Returns the attempt number won \(0 when the claim was refused\), which the caller passes back to MarkRunAlertDelivered so a superseded send cannot stamp.

<a name="SchedulerStore.ConfirmInfraFailure"></a>
### func \(\*SchedulerStore\) [ConfirmInfraFailure](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L328>)

```go
func (s *SchedulerStore) ConfirmInfraFailure(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
```

ConfirmInfraFailure stamps a provisional infra mark confirmed, guarded on the exact attempt and on the mark still being provisional. It reports whether a row was confirmed.

<a name="SchedulerStore.CountHeartbeatFleet"></a>
### func \(\*SchedulerStore\) [CountHeartbeatFleet](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1022>)

```go
func (s *SchedulerStore) CountHeartbeatFleet(ctx context.Context, staleBefore time.Time) (executor.HeartbeatFleet, error)
```

CountHeartbeatFleet counts the running TIs with a heartbeat and those whose last heartbeat is before staleBefore, for the agent\-lost reaper's mass\-silence guard \(\#1624\). It implements executor.HeartbeatFleetCounter.

<a name="SchedulerStore.CreateScheduledRun"></a>
### func \(\*SchedulerStore\) [CreateScheduledRun](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L824>)

```go
func (s *SchedulerStore) CreateScheduledRun(ctx context.Context, tenantID, dagID string, logical time.Time) error
```

CreateScheduledRun inserts a scheduled run for the DAG dagID owned by the tenant tenantID \(a tenant UUID\), idempotent on run\_id. The tenant is explicit because a dag\_id is unique only within its tenant \(\#209\). A tenant at its max\_runs\_per\_day gets domain.ErrLimitExceeded and no run.

<a name="SchedulerStore.DispatchAttempts"></a>
### func \(\*SchedulerStore\) [DispatchAttempts](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L640>)

```go
func (s *SchedulerStore) DispatchAttempts(ctx context.Context, runID, taskID string) (attempts int, active bool, err error)
```

DispatchAttempts returns the consecutive dispatch\-failure count of a task still scheduled or queued; active is false when it has moved on.

<a name="SchedulerStore.FailDispatchExhausted"></a>
### func \(\*SchedulerStore\) [FailDispatchExhausted](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L657>)

```go
func (s *SchedulerStore) FailDispatchExhausted(ctx context.Context, runID, taskID, reason string) error
```

FailDispatchExhausted fails a scheduled task as dispatch\_failed once its dispatch\-attempt budget is spent \(ADR 0031 Amendment A\).

<a name="SchedulerStore.FailDispatchRefused"></a>
### func \(\*SchedulerStore\) [FailDispatchRefused](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L673>)

```go
func (s *SchedulerStore) FailDispatchRefused(ctx context.Context, runID, taskID, reason string) error
```

FailDispatchRefused fails a task whose dispatch the executor refused \(ADR 0066 section 3\) and spends its retry budget, so a permanent refusal is not dispatched and refused again once per retry. It serves both the sync path \(a scheduled task\) and the buffered path \(a scheduled or queued one\).

<a name="SchedulerStore.FailStoppedTenantTaskInstances"></a>
### func \(\*SchedulerStore\) [FailStoppedTenantTaskInstances](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1334>)

```go
func (s *SchedulerStore) FailStoppedTenantTaskInstances(ctx context.Context, tenantID string) (int64, error)
```

FailStoppedTenantTaskInstances fails the queued and running task instances of the tenant while it is suspended in stop mode, and returns how many it failed \(scheduler.TenantStopper, \#2181\). Once the suspension is lifted or changed to another mode it fails nothing.

<a name="SchedulerStore.ListActiveStagingVolumes"></a>
### func \(\*SchedulerStore\) [ListActiveStagingVolumes](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1288>)

```go
func (s *SchedulerStore) ListActiveStagingVolumes(ctx context.Context) ([]domain.StagingVolumeState, error)
```

ListActiveStagingVolumes returns active staging volumes joined with their DAG run's state \(empty when the run row is gone\), for the GC \(ADR 0022\).

<a name="SchedulerStore.ListAgentLostCandidates"></a>
### func \(\*SchedulerStore\) [ListAgentLostCandidates](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L985>)

```go
func (s *SchedulerStore) ListAgentLostCandidates(ctx context.Context) ([]executor.AgentLostCandidate, error)
```

ListAgentLostCandidates returns every \`running\` TI with a non\-null last\_heartbeat\_at, for the scheduler's TI heartbeat reaper \(\#128\). The reaper applies the threshold per row so the SQL stays simple.

<a name="SchedulerStore.ListBusyWarmWorkerPods"></a>
### func \(\*SchedulerStore\) [ListBusyWarmWorkerPods](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1158>)

```go
func (s *SchedulerStore) ListBusyWarmWorkerPods(ctx context.Context) (map[string]bool, error)
```

ListBusyWarmWorkerPods returns the set of warm\-worker pod names currently serving a \`running\` attempt \(ADR 0058 N1d\-b\): a warm worker is BUSY iff some \`running\` task\_instance is durably bound to it \(warm\_worker\_id = the pod's own name\). The busy\-aware warm\-pool reconciler reads this once per tick to classify each live warm pod as busy or idle, so scale\-down/drain deletes only IDLE workers and never kills an in\-flight attempt \(review findings M1/M2\). With warm pools off no TI is ever bound, so the set is always empty and every worker classifies as idle — byte\-for\-byte today. Implements executor.BusyWarmWorkerSource without the executor importing storage.

<a name="SchedulerStore.ListProvisionalInfraFailures"></a>
### func \(\*SchedulerStore\) [ListProvisionalInfraFailures](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L307>)

```go
func (s *SchedulerStore) ListProvisionalInfraFailures(ctx context.Context) ([]executor.ProvisionalInfraFailure, error)
```

ListProvisionalInfraFailures returns the provisional infra marks \(failed, infra, not yet confirmed\) of queued or running runs, oldest first and bounded, for the reconciler's confirmation pass.

<a name="SchedulerStore.ListReapCandidates"></a>
### func \(\*SchedulerStore\) [ListReapCandidates](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L898>)

```go
func (s *SchedulerStore) ListReapCandidates(ctx context.Context) ([]executor.ReapCandidate, error)
```

ListReapCandidates returns every dag\_run currently in 'running' state with the timestamp of its most recent activity, for the scheduler's orphan reaper. The query \(sqlc.runs.ListOrphanCandidates\) is the authority on how to compute the timestamp; the reaper only decides whether each one is past its threshold.

<a name="SchedulerStore.ListRunningTasks"></a>
### func \(\*SchedulerStore\) [ListRunningTasks](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1216>)

```go
func (s *SchedulerStore) ListRunningTasks(ctx context.Context, grace time.Duration, after executor.ReapCursor) ([]executor.PodLostCandidate, error)
```

ListRunningTasks returns the \`running\`, non\-warm TIs that have been running for at least grace, with the timestamp each entered running, for the pod\-lost reaper \(\#527\), one page after the cursor \(R\-07\). The reaper still applies the pod\-liveness check per row.

<a name="SchedulerStore.ListStaleQueuedCandidates"></a>
### func \(\*SchedulerStore\) [ListStaleQueuedCandidates](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1092>)

```go
func (s *SchedulerStore) ListStaleQueuedCandidates(ctx context.Context, threshold time.Duration, after executor.ReapCursor) ([]executor.StaleQueuedCandidate, error)
```

ListStaleQueuedCandidates returns one page of the TIs \`queued\` for at least threshold, with their queued\_at, after the cursor, for the dispatch\-lost reaper \(\#202, R\-07\). The reaper re\-checks the threshold per row.

<a name="SchedulerStore.ListWarmBoundRunningTIs"></a>
### func \(\*SchedulerStore\) [ListWarmBoundRunningTIs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1128>)

```go
func (s *SchedulerStore) ListWarmBoundRunningTIs(ctx context.Context) ([]executor.WarmBoundTI, error)
```

ListWarmBoundRunningTIs returns every \`running\` TI durably bound to a warm worker \(warm\_worker\_id IS NOT NULL\), for the warm\-worker\-lost reaper \(ADR 0058 N1d\-a2\). With warm pools off no TI is ever bound, so this is always empty and the reaper is inert.

<a name="SchedulerStore.MarkQueued"></a>
### func \(\*SchedulerStore\) [MarkQueued](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L597>)

```go
func (s *SchedulerStore) MarkQueued(ctx context.Context, runID, taskID string, expectNextDispatchAt *time.Time) (bool, error)
```

MarkQueued moves a task from the scheduled slot this tick planned to queued after its dispatch was accepted. expectNextDispatchAt is the next\_dispatch\_at the tick read \(nil when unset\). It reports false, with no error, when the row has moved on: a buffered worker already failed or re\-offered the task, or the agent already reported \(see MarkTaskInstanceQueued\).

<a name="SchedulerStore.MarkRunAlertDelivered"></a>
### func \(\*SchedulerStore\) [MarkRunAlertDelivered](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L769>)

```go
func (s *SchedulerStore) MarkRunAlertDelivered(ctx context.Context, runID string, attempt int) error
```

MarkRunAlertDelivered stamps a run's on\-failure alert as delivered, for the attempt the caller won. The attempt is part of the predicate so a stamp from a send that an operator clear has since superseded matches no row — see the query.

<a name="SchedulerStore.MarkStagingDeleted"></a>
### func \(\*SchedulerStore\) [MarkStagingDeleted](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L883>)

```go
func (s *SchedulerStore) MarkStagingDeleted(ctx context.Context, pvcName, reason string) error
```

MarkStagingDeleted records that a staging volume's PVC was deleted and why \(run\_succeeded | ttl\_expired | orphaned\).

<a name="SchedulerStore.MarkTaskAgentLost"></a>
### func \(\*SchedulerStore\) [MarkTaskAgentLost](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1054>)

```go
func (s *SchedulerStore) MarkTaskAgentLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
```

MarkTaskAgentLost transitions one TI to \`failed\` with the agent\_lost reason. The WHERE state='running' guard makes this idempotent and prevents a late terminal report being overwritten: if the row already moved, we touch zero rows and return nil. It is pinned to the listed attempt, \(tryNumber, attemptEpoch\) \(ADR 0051 amendment\): a mark computed for a superseded execution is a no\-op on its replacement.

<a name="SchedulerStore.MarkTaskCredentialCeiling"></a>
### func \(\*SchedulerStore\) [MarkTaskCredentialCeiling](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1075>)

```go
func (s *SchedulerStore) MarkTaskCredentialCeiling(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
```

MarkTaskCredentialCeiling fails one running TI whose attempt outlived auth.max\_attempt\_credential\_lifetime \(\#1461\) as a task failure with the credential\_ceiling reason: no infra kind, so the planner applies the task's retry policy instead of re\-placing it, and no provisional mark, since there is no infra guess for the reconciler to confirm. Guarded and pinned to the listed attempt exactly like MarkTaskAgentLost.

<a name="SchedulerStore.MarkTaskDispatchFailed"></a>
### func \(\*SchedulerStore\) [MarkTaskDispatchFailed](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1036>)

```go
func (s *SchedulerStore) MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
```

MarkTaskDispatchFailed transitions a TI to \`failed\` after its asynchronous dispatch failed inside the BufferedDispatcher worker \(\#127\). The SQL guard only targets scheduled/queued rows, so a TI that already moved to running or terminal between the worker accepting the request and the dispatch failing is left alone \(defense in depth — the agent's late progress report wins over the dispatcher's "I failed" claim\).

<a name="SchedulerStore.MarkTaskDispatchLost"></a>
### func \(\*SchedulerStore\) [MarkTaskDispatchLost](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1177>)

```go
func (s *SchedulerStore) MarkTaskDispatchLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
```

MarkTaskDispatchLost transitions one TI to \`failed\` with the dispatch\_lost reason. The WHERE state='queued' guard makes this idempotent: a TI that has since been dispatched \(real progress landed\) is left alone, and so is a row on a different \(tryNumber, attemptEpoch\) than the one listed \(ADR 0051 amendment\). It returns whether a row was actually updated.

<a name="SchedulerStore.MarkTaskPodLost"></a>
### func \(\*SchedulerStore\) [MarkTaskPodLost](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1252>)

```go
func (s *SchedulerStore) MarkTaskPodLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
```

MarkTaskPodLost transitions one TI to \`failed\` with the pod\_lost reason. The WHERE state='running' guard makes it idempotent: a TI that has since moved on \(a late terminal report landed\) is left alone, and so is a row on a different \(tryNumber, attemptEpoch\) than the one listed \(ADR 0051 amendment\).

<a name="SchedulerStore.MarkTaskPodLostOOMKilled"></a>
### func \(\*SchedulerStore\) [MarkTaskPodLostOOMKilled](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1264>)

```go
func (s *SchedulerStore) MarkTaskPodLostOOMKilled(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
```

MarkTaskPodLostOOMKilled is MarkTaskPodLost for an attempt whose warm worker's container the kubelet reported OOMKilled: the same infra mark and guards, with a failure message that names the OOM kill \(\#1218\).

<a name="SchedulerStore.MarkTaskStartExpired"></a>
### func \(\*SchedulerStore\) [MarkTaskStartExpired](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L1197>)

```go
func (s *SchedulerStore) MarkTaskStartExpired(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
```

MarkTaskStartExpired fails one queued TI whose task pod never started before its attempt credential ran out, as an infra failure the planner re\-places off the retry budget with a fresh credential \(\#1126\). Same guards as MarkTaskDispatchLost: state='queued' and the listed \(tryNumber, attemptEpoch\). It returns whether a row was actually updated.

<a name="SchedulerStore.MaterializeTasks"></a>
### func \(\*SchedulerStore\) [MaterializeTasks](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L363>)

```go
func (s *SchedulerStore) MaterializeTasks(ctx context.Context, runID string, tasks []domain.TaskSpec) error
```

MaterializeTasks creates a none\-state task instance for each task in the run, in one batched COPY rather than T INSERT round\-trips. The rows are identical to the per\-task loop this replaced: try\_number pinned to 1, state none, and max\_tries derived from the task's retries \(default 1\). An empty task set is a no\-op \(a DAG with no tasks materializes nothing\).

<a name="SchedulerStore.PoolBudgets"></a>
### func \(\*SchedulerStore\) [PoolBudgets](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L424>)

```go
func (s *SchedulerStore) PoolBudgets(ctx context.Context) (map[string]int, error)
```

PoolBudgets returns every named pool's slot cap keyed by scheduler.PoolKey\(tenantID, name\) — the cross\-DAG admission budget the pool gate enforces \(ADR 0053 Stage 3\). The scheduler calls it once per tick, and only on the Pro path; Lite never loads pool budgets.

<a name="SchedulerStore.ReapRun"></a>
### func \(\*SchedulerStore\) [ReapRun](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L937>)

```go
func (s *SchedulerStore) ReapRun(ctx context.Context, runID string, quietBefore time.Time) (bool, error)
```

ReapRun fails an orphaned dag run, then any of its still\-active task instances, inside a single transaction, and reports whether it did. The list the reaper decided from is only a snapshot, so the orphan predicate is re\-checked atomically here: the run's task instances are share\-locked first, without waiting \(a task instance being written counts as activity and makes the reap a no\-op\), then the run UPDATE applies the same predicate ListReapCandidates does \(still running, no live task instance, last activity at or before quietBefore\). If zero rows are touched the run is no longer an orphan \(a task instance moved, fresh activity landed, or a competing finalizer settled it\), the transaction rolls back without touching any task instance, and ReapRun returns false so the caller leaves the run's pods alone. This guarantees we cannot leave a run as \`success\`/\`failed\` while flipping its TIs to \`failed \(orphaned\)\`, nor fail a run that resumed between the list and the reap. Idempotent: a second call on an already\-failed run returns false.

<a name="SchedulerStore.RecordDispatchBackpressure"></a>
### func \(\*SchedulerStore\) [RecordDispatchBackpressure](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L551>)

```go
func (s *SchedulerStore) RecordDispatchBackpressure(ctx context.Context, runID, taskID string, nextAt time.Time) error
```

RecordDispatchBackpressure backs off a scheduled task after a retriable\-forever cluster\-backpressure dispatch failure \(quota 403 / APF 429\), setting nextAt WITHOUT incrementing dispatch\_attempts so it never accumulates toward the dispatch\_failed cap \(ADR 0053\).

<a name="SchedulerStore.RecordDispatchFailure"></a>
### func \(\*SchedulerStore\) [RecordDispatchFailure](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L535>)

```go
func (s *SchedulerStore) RecordDispatchFailure(ctx context.Context, runID, taskID string, nextAt time.Time) error
```

RecordDispatchFailure increments a scheduled task's dispatch\-failure counter and backs off its next attempt to nextAt \(ADR 0031 Amendment A\).

<a name="SchedulerStore.RecordDispatchUnavailable"></a>
### func \(\*SchedulerStore\) [RecordDispatchUnavailable](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L571>)

```go
func (s *SchedulerStore) RecordDispatchUnavailable(ctx context.Context, runID, taskID string, now time.Time, minDelay, maxDelay time.Duration) (since, nextAt time.Time, applied bool, err error)
```

RecordDispatchUnavailable re\-offers a task whose pod create failed because the Kubernetes API was unavailable \(\#1606\): back to scheduled, WITHOUT incrementing dispatch\_attempts, held for as long as the task has already been riding the outage out, clamped to \[minDelay, maxDelay\]. It returns the start of the task's outage window, stamped with now on the first such failure of the episode, so the caller can fail the task once the window passes its bound \(ADR 0093\), and the next\_dispatch\_at it wrote. applied is false when the task is no longer scheduled or queued.

<a name="SchedulerStore.RecordStagingVolume"></a>
### func \(\*SchedulerStore\) [RecordStagingVolume](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L868>)

```go
func (s *SchedulerStore) RecordStagingVolume(ctx context.Context, tenantID, dagID, runID, pvcName, size string) error
```

RecordStagingVolume records a per\-run staging volume as active, keyed by PVC name \(idempotent — called per task as the PVC is ensured\). ADR 0022.

<a name="SchedulerStore.RedispatchReschedule"></a>
### func \(\*SchedulerStore\) [RedispatchReschedule](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L687>)

```go
func (s *SchedulerStore) RedispatchReschedule(ctx context.Context, runID, taskID string) error
```

RedispatchReschedule returns a task parked in up\_for\_reschedule to 'none' for re\-dispatch, preserving try\_number \(reschedule is not a retry; \#380\).

<a name="SchedulerStore.RequeueDispatch"></a>
### func \(\*SchedulerStore\) [RequeueDispatch](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L619>)

```go
func (s *SchedulerStore) RequeueDispatch(ctx context.Context, runID, taskID string, countAttempt, clearUnavailable bool, nextAt time.Time) (bool, error)
```

RequeueDispatch re\-offers a task whose buffered dispatch failed in the worker: back to scheduled, held until nextAt, with one more dispatch attempt when countAttempt. clearUnavailable closes the outage window of ADR 0093 \(the apiserver answered the create\). It reports false when the task is no longer scheduled or queued.

<a name="SchedulerStore.ResetForInfraReplace"></a>
### func \(\*SchedulerStore\) [ResetForInfraReplace](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L517>)

```go
func (s *SchedulerStore) ResetForInfraReplace(ctx context.Context, runID, taskID string) (bool, error)
```

ResetForInfraReplace returns a task instance failed by a reaper as infra \(last\_failure\_kind='infra'\) to 'none' so the scheduler re\-runs it, bumping infra\_attempts instead of try\_number — an infrastructure fault must not consume the user's retry budget \(ADR 0051 Phase 1\). It uses the failed\+infra\-guarded query so a late terminal report or a non\-infra failure at state='failed' cannot be re\-placed off\-budget. The bool reports whether the guarded update fired \(exactly one row\): a false means the TI was no longer a failed\-infra candidate, so the caller must not record a re\-placement it did not perform.

<a name="SchedulerStore.ResetForRetry"></a>
### func \(\*SchedulerStore\) [ResetForRetry](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L493>)

```go
func (s *SchedulerStore) ResetForRetry(ctx context.Context, runID, taskID string) (bool, error)
```

ResetForRetry returns a task instance to 'none', clears its timestamps, and increments its try number so the scheduler re\-evaluates and re\-runs it. It uses the up\_for\_retry\-guarded query so a stale retry decision cannot reset a TI that has since been re\-dispatched \(audit follow\-up; see the query doc\). The bool reports whether the guarded update actually fired \(exactly one row\): a false means the TI was no longer up\_for\_retry and nothing was reset, so the caller must not record a retry it did not perform.

<a name="SchedulerStore.ScheduledDAGs"></a>
### func \(\*SchedulerStore\) [ScheduledDAGs](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L782>)

```go
func (s *SchedulerStore) ScheduledDAGs(ctx context.Context, now time.Time) ([]scheduler.ScheduledDAG, error)
```

ScheduledDAGs lists the scheduled DAGs that may be due at now: those with no stored next due time and those whose time is at or before now \(\#2175\).

<a name="SchedulerStore.SetNextRunAt"></a>
### func \(\*SchedulerStore\) [SetNextRunAt](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L807>)

```go
func (s *SchedulerStore) SetNextRunAt(ctx context.Context, tenantID, dagID string, epoch int64, at time.Time) error
```

SetNextRunAt stores when the DAG is next due, unless its schedule epoch moved past epoch since it was listed: a new version or a deleted run cleared the time, and the next tick computes it again from the DAG as it is now.

<a name="SchedulerStore.SetProvisionalInfraMarks"></a>
### func \(\*SchedulerStore\) [SetProvisionalInfraMarks](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L302>)

```go
func (s *SchedulerStore) SetProvisionalInfraMarks(on bool)
```

SetProvisionalInfraMarks makes every reaper infra mark provisional until the pod reconciler confirms it \(ADR 0052 amendment, part 2\). main.go calls it only when the Kubernetes reconciler is wired with this store as its confirmer; without a confirmer a provisional mark would wait for the liveness valve on every reap.

<a name="SchedulerStore.SetRunState"></a>
### func \(\*SchedulerStore\) [SetRunState](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L701>)

```go
func (s *SchedulerStore) SetRunState(ctx context.Context, runID string, from, to domain.DagRunState) (bool, error)
```

SetRunState moves a run from the state the caller read to another one. It reports false when no row moved: the run is no longer in from \(a clear re\-opened it, a user marked it\), so it is left as it is \(\#2047\).

<a name="SchedulerStore.SetTaskNote"></a>
### func \(\*SchedulerStore\) [SetTaskNote](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L474>)

```go
func (s *SchedulerStore) SetTaskNote(ctx context.Context, runID, taskID, note string) error
```

SetTaskNote attaches operational context to a task instance, shown in the UI.

<a name="SchedulerStore.SetWarmExecution"></a>
### func \(\*SchedulerStore\) [SetWarmExecution](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L291>)

```go
func (s *SchedulerStore) SetWarmExecution(exec config.ExecutionSection, policy *domain.ExecutionPolicy, placement domain.WarmPolicy)
```

SetWarmExecution records the operator's warm\-pool config so ActiveWarmTargets can resolve each active dag\_version's effective warm target, the execution allowlist so it skips a version whose image the allowlist refuses \(nil allows every image\), and the warm placement decision dispatch applies so it skips a version none of whose tasks dispatch would place on a warm worker \(\#2065\). main.go calls it only when warm pools are enabled; left unset, warm pools read as off \(every target 0\).

<a name="SchedulerStore.StartQueuedRun"></a>
### func \(\*SchedulerStore\) [StartQueuedRun](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/scheduler_store.go#L717>)

```go
func (s *SchedulerStore) StartQueuedRun(ctx context.Context, runID, tenantID string) (bool, error)
```

StartQueuedRun moves a queued run of the tenant to running, guarded on the run still being queued \(\#1766\). It reports false when no row moved: a user marked or deleted the run after the tick read it, so it must not start.

<a name="SchemaWait"></a>
## type [SchemaWait](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/schema_check.go#L59-L74>)

SchemaWait is the boot gate's verdict: whether this binary can build the rest of the process on the schema it found, and if not, why.

```go
type SchemaWait struct {
    // Class is one of the SchemaReason constants, empty when the schema is
    // current.
    Class string
    // Reason is the text /readyz and the log carry, with the versions:
    // "schema behind v40, requires v46". It never holds the DSN or a host,
    // since /readyz is unauthenticated.
    Reason string
    // Ahead is a clean schema newer than this binary, accepted with a warning.
    Ahead bool
    // Err is why the schema could not be read, for the log line only.
    Err error
    // Detail is more for the log line only: for a replaced database, the
    // installation ids on both sides, which /readyz does not carry.
    Detail string
}
```

<a name="SchemaWait.Ready"></a>
### func \(SchemaWait\) [Ready](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/schema_check.go#L77>)

```go
func (w SchemaWait) Ready() bool
```

Ready reports whether the schema is one this binary can serve.

<a name="SecretValue"></a>
## type [SecretValue](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L57-L66>)

SecretValue is one non\-empty encrypted column of one row.

```go
type SecretValue struct {
    // Column is the registry entry the value belongs to.
    Column EncryptedColumn
    // RowID is the row's primary key, as text.
    RowID string
    // Label names the row for an operator (the conn_id).
    Label string
    // Ciphertext is the stored value.
    Ciphertext string
}
```

<a name="ReadSecretValues"></a>
### func [ReadSecretValues](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L77>)

```go
func ReadSecretValues(ctx context.Context, q RowQuerier) ([]SecretValue, error)
```

ReadSecretValues returns every non\-empty value of every registered encrypted column, across every tenant. A NULL or empty column is not a secret and is skipped. A registered table that does not exist yet \(a datastore whose schema predates it\) holds no values.

<a name="SecretValuesNotUnder"></a>
### func [SecretValuesNotUnder](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L139>)

```go
func SecretValuesNotUnder(ctx context.Context, q RowQuerier, c secrets.Cipher) ([]SecretValue, error)
```

SecretValuesNotUnder returns every non\-empty encrypted value that c ALONE does not open. It is the post\-commit re\-check of a key migration \(ADR 0065 step 5\): a predecessor may leave the config only when this is empty.

<a name="TaskWaitStore"></a>
## type [TaskWaitStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/task_wait.go#L19-L21>)

TaskWaitStore reads the waiting task ages behind dexaflow\_task\_oldest\_waiting\_seconds \(\#2300\).

```go
type TaskWaitStore struct {
    // contains filtered or unexported fields
}
```

<a name="NewTaskWaitStore"></a>
### func [NewTaskWaitStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/task_wait.go#L24>)

```go
func NewTaskWaitStore(pg *Postgres) *TaskWaitStore
```

NewTaskWaitStore builds a TaskWaitStore over pg.

<a name="TaskWaitStore.OldestWaiting"></a>
### func \(\*TaskWaitStore\) [OldestWaiting](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/task_wait.go#L30>)

```go
func (s *TaskWaitStore) OldestWaiting(ctx context.Context) ([]WaitingAge, error)
```

OldestWaiting returns one age per tenant and waiting state that has at least one task waiting.

<a name="TxBeginner"></a>
## type [TxBeginner](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L210-L212>)

TxBeginner opens a transaction. \*pgx.Conn and \*pgxpool.Pool satisfy it.

```go
type TxBeginner interface {
    Begin(ctx context.Context) (pgx.Tx, error)
}
```

<a name="UnreadableSecretsError"></a>
## type [UnreadableSecretsError](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L192-L195>)

UnreadableSecretsError lists values that no given key opens. Their ciphertext is the only copy of a credential, so the pass that found them changes nothing.

```go
type UnreadableSecretsError struct {
    // Values are the columns no key opened.
    Values []SecretValue
}
```

<a name="UnreadableSecretsError.Error"></a>
### func \(\*UnreadableSecretsError\) [Error](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/keymigration.go#L197>)

```go
func (e *UnreadableSecretsError) Error() string
```



<a name="UsageStore"></a>
## type [UsageStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/usage_store.go#L19-L22>)

UsageStore reads and writes the task attempt usage events of ADR 0096. The events themselves are written by a trigger on task\_instances \(migration 055\); this store drives the switch that trigger reads and serves them.

```go
type UsageStore struct {
    // contains filtered or unexported fields
}
```

<a name="NewUsageStore"></a>
### func [NewUsageStore](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/usage_store.go#L25>)

```go
func NewUsageStore(pg *Postgres) *UsageStore
```

NewUsageStore builds a UsageStore over the given Postgres connection.

<a name="UsageStore.EventsLag"></a>
### func \(\*UsageStore\) [EventsLag](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/usage_store.go#L102>)

```go
func (s *UsageStore) EventsLag(ctx context.Context) (time.Duration, error)
```

EventsLag is the age of the oldest usage event the route cannot serve yet because a transaction older than it is still open, or 0 when there is none.

<a name="UsageStore.Horizon"></a>
### func \(\*UsageStore\) [Horizon](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/usage_store.go#L112>)

```go
func (s *UsageStore) Horizon(ctx context.Context) (domain.UsageCursor, error)
```

Horizon is the cursor a new reader of only future events starts at: every event served now sorts before it, and every event committed later after.

<a name="UsageStore.ReadEvents"></a>
### func \(\*UsageStore\) [ReadEvents](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/usage_store.go#L41>)

```go
func (s *UsageStore) ReadEvents(ctx context.Context, after domain.UsageCursor, limit int) (domain.UsagePage, error)
```

ReadEvents returns up to limit usage events after the cursor, in serving order. The switch, the pruned mark and the events are read in one repeatable\-read snapshot, and the janitor moves the mark in the statement that deletes, so a page that is not Gone never skips a deleted event. The zero cursor reads from the oldest event kept and is never Gone.

<a name="UsageStore.SetEventsEnabled"></a>
### func \(\*UsageStore\) [SetEventsEnabled](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/usage_store.go#L31>)

```go
func (s *UsageStore) SetEventsEnabled(ctx context.Context, enabled bool) (bool, error)
```

SetEventsEnabled turns the recording of usage events on or off for the whole database. It reports whether the stored value changed.

<a name="VariableEncryption"></a>
## type [VariableEncryption](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/variable_crypto.go#L24>)

VariableEncryption is how the server stores Variable values \(\#507\).

```go
type VariableEncryption string
```

<a name="VariableEncryptionOff"></a>

```go
const (
    // VariableEncryptionOff writes plaintext, as every release before it did.
    // It still reads values an earlier "on" sealed, and leaves them sealed.
    VariableEncryptionOff VariableEncryption = "off"
    // VariableEncryptionOn seals every write with the secret key, and the boot
    // sweep seals the plaintext rows already stored.
    VariableEncryptionOn VariableEncryption = "on"
    // VariableEncryptionDecrypt writes plaintext and the boot sweep restores
    // every sealed row to plaintext: the step before rolling back to a release
    // that cannot read sealed values.
    VariableEncryptionDecrypt VariableEncryption = "decrypt"
)
```

<a name="ParseVariableEncryption"></a>
### func [ParseVariableEncryption](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/variable_crypto.go#L41>)

```go
func ParseVariableEncryption(s string) (VariableEncryption, error)
```

ParseVariableEncryption reads the variable\_encryption setting. Empty is the default, off; an unknown value is an error rather than a silent off.

<a name="VariableSweepResult"></a>
## type [VariableSweepResult](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/variable_crypto.go#L137-L145>)

VariableSweepResult counts what one variable\_encryption boot sweep did.

```go
type VariableSweepResult struct {
    // Migrated is the number of rows rewritten into the configured form.
    Migrated int
    // Skipped is the number of rows a concurrent write changed between the
    // sweep's read and its write; that write's form stands.
    Skipped int
    // Unreadable is the number of sealed rows no configured key opens.
    Unreadable int
}
```

<a name="VariableSweepResult.Clean"></a>
### func \(VariableSweepResult\) [Clean](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/variable_crypto.go#L148>)

```go
func (r VariableSweepResult) Clean() bool
```

Clean reports whether the sweep left nothing behind.

<a name="WaitingAge"></a>
## type [WaitingAge](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/task_wait.go#L11-L15>)

WaitingAge is the age of the oldest task instance of a tenant that waits in State \(scheduled or queued\).

```go
type WaitingAge struct {
    Tenant     string
    State      string
    AgeSeconds float64
}
```

<a name="XComIndex"></a>
## type [XComIndex](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/xcom_index.go#L17-L20>)

XComIndex is the Postgres\-backed XCom metadata index. It implements xcom.Index, recording each pushed value so the API can find and list it.

```go
type XComIndex struct {
    // contains filtered or unexported fields
}
```

<a name="NewXComIndex"></a>
### func [NewXComIndex](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/xcom_index.go#L23>)

```go
func NewXComIndex(pg *Postgres) *XComIndex
```

NewXComIndex builds an XComIndex over the given Postgres connection.

<a name="XComIndex.PurgeExpired"></a>
### func \(\*XComIndex\) [PurgeExpired](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/xcom_index.go#L53>)

```go
func (x *XComIndex) PurgeExpired(ctx context.Context) error
```

PurgeExpired deletes xcom\_index rows past their expiry. Redis expires the values natively; this reclaims the metadata rows. A backlog of expired rows can take longer than the API statement timeout, which the janitor shares when the scheduler has no pool of its own, so it runs without it.

<a name="XComIndex.RecordXCom"></a>
### func \(\*XComIndex\) [RecordXCom](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/xcom_index.go#L28>)

```go
func (x *XComIndex) RecordXCom(ctx context.Context, e xcom.IndexEntry) error
```

RecordXCom upserts the metadata for a pushed XCom value.

<a name="XComReader"></a>
## type [XComReader](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/xcom_index.go#L61-L64>)

XComReader reads XCom values for the API: it resolves the Redis key from the Postgres index by name and fetches the value from the backend.

```go
type XComReader struct {
    // contains filtered or unexported fields
}
```

<a name="NewXComReader"></a>
### func [NewXComReader](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/xcom_index.go#L68>)

```go
func NewXComReader(pg *Postgres, backend xcom.Backend) *XComReader
```

NewXComReader builds an XComReader over the given Postgres connection and XCom backend.

<a name="XComReader.GetXCom"></a>
### func \(\*XComReader\) [GetXCom](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/xcom_index.go#L74>)

```go
func (r *XComReader) GetXCom(ctx context.Context, tenant, dagID, runID, taskID, key string) (xcom.Entry, error)
```

GetXCom returns the XCom entry for the named value, or domain.ErrNotFound when it is absent or expired \(in the index or in Redis\).

<a name="XComReader.ListXComEntries"></a>
### func \(\*XComReader\) [ListXComEntries](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/xcom_index.go#L96>)

```go
func (r *XComReader) ListXComEntries(ctx context.Context, tenant, dagID, runID, taskID string) ([]domain.XComEntryMeta, error)
```

ListXComEntries returns the metadata of every non\-expired XCom pushed by a task instance \(keys and timestamps, no values\), for the XCom list view.

<a name="XComValueDeleter"></a>
## type [XComValueDeleter](<https://github.com/dexadata/dexaflow/blob/main/internal/storage/repository.go#L53-L55>)

XComValueDeleter deletes a stored XCom value by its backend key. xcom.Backend satisfies it \(Redis in production, xcom\_store under Lite\).

```go
type XComValueDeleter interface {
    Delete(ctx context.Context, key string) error
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
