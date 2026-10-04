-- One CONCURRENTLY statement, no transaction: see the up migration. If it is
-- interrupted, re-run the drop by hand, then `migrate force 34`.
DROP INDEX CONCURRENTLY IF EXISTS idx_ti_active_tenant;
