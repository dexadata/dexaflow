-- 046_dag_versions_build_info.down.sql
-- Drop the build info of DAG versions. The previous release never reads it;
-- the recorded provenance is lost (the register audit entries keep a copy).
BEGIN;
SET LOCAL lock_timeout = '5s';
ALTER TABLE dag_versions
    DROP COLUMN IF EXISTS build_info;
COMMIT;
