-- Undo 044: drop the API tokens and the built-in deployer role.
--
-- Dropping api_tokens revokes every API token at once. The deployer role is
-- removed only where it is built in (is_system); a custom role of that name
-- is the tenant's own and stays. Its user_roles and role_permissions rows
-- cascade. The deploy:dag permission goes last, once no built-in role
-- references it; a custom role granted deploy:dag loses that grant with it.
BEGIN;

DROP TABLE IF EXISTS api_tokens;

DELETE FROM roles WHERE name = 'deployer' AND is_system;

DELETE FROM permissions WHERE action = 'deploy' AND resource = 'dag';

COMMIT;
