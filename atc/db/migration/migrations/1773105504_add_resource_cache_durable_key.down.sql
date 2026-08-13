DROP INDEX IF EXISTS resource_caches_durable_key_idx;
ALTER TABLE resource_caches DROP COLUMN durable_key;
