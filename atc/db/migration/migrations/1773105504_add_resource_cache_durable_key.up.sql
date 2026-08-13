-- durable_key names a resource cache by its CONTENT, so a durable copy of it
-- can outlive the row.
--
-- resource_caches.id is a sequence, and CleanUpInvalidCaches hard-deletes a row
-- the moment nothing references it -- which is exactly when a long-term copy is
-- supposed to still be useful. The next build re-inserts the same
-- (config, version, params) tuple and gets a new id, so anything stored under
-- the old id is both unreachable and unreclaimable. Worse, restoring the
-- database rewinds the sequence, and a re-minted id can name a different tuple
-- entirely.
--
-- The key is sha256 over the resource type identity, the source, the version
-- digest and the params hash, so it is stable across delete-and-recreate and
-- identical on every cluster that asks for the same thing.
--
-- Nullable, and rows written before this migration keep NULL: the key cannot be
-- recomputed here because the source lives in resource_configs and the custom
-- type chain has to be walked in Go. A NULL key simply means "not eligible for
-- durable storage", and the next FindOrCreateResourceCache for that tuple fills
-- it in.
ALTER TABLE resource_caches ADD COLUMN durable_key text;

CREATE INDEX resource_caches_durable_key_idx ON resource_caches (durable_key)
  WHERE durable_key IS NOT NULL;
