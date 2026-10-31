-- The idtoken credential manager and pipeline var_sources are gone. Nothing
-- signs with or publishes signing_keys any more, no pipeline declares
-- var_sources, and pipelines.nonce only ever encrypted var_sources. The
-- signing key lifecycler's component row has no runner left to claim it.
DROP TABLE signing_keys;

ALTER TABLE pipelines DROP COLUMN var_sources,
                      DROP COLUMN nonce;

DELETE FROM components WHERE name = 'signing_key_lifecycler';
