-- A deprecated scope keeps its resource_config_id and has resource_id NULL, so
-- the global-scope index counted it as the config's global scope: a second
-- deprecation of a scope for the same config violated the index, and the global
-- ON CONFLICT adopted the deprecated row. Only a live scope is a global scope.
DROP INDEX resource_config_scopes_resource_config_id_uniq;

CREATE UNIQUE INDEX resource_config_scopes_resource_config_id_uniq
ON resource_config_scopes (resource_config_id)
WHERE resource_id IS NULL AND deprecated_at IS NULL;
