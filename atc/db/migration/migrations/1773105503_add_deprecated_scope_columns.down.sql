DROP INDEX IF EXISTS resource_config_scopes_deprecated_from_resource_id;
DROP INDEX IF EXISTS resource_config_scopes_deprecated_at;

ALTER TABLE resource_config_scopes
    DROP COLUMN IF EXISTS deprecated_from_resource_id,
    DROP COLUMN IF EXISTS deprecated_at;
