ALTER TABLE resource_config_scopes
    ADD COLUMN deprecated_at TIMESTAMP WITH TIME ZONE,
    ADD COLUMN deprecated_from_resource_id INT REFERENCES resources(id) ON DELETE SET NULL;

CREATE INDEX resource_config_scopes_deprecated_at
    ON resource_config_scopes (deprecated_at)
    WHERE deprecated_at IS NOT NULL;

CREATE INDEX resource_config_scopes_deprecated_from_resource_id
    ON resource_config_scopes (deprecated_from_resource_id)
    WHERE deprecated_from_resource_id IS NOT NULL;
