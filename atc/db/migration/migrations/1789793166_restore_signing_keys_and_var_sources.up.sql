-- The idtoken credential manager and pipeline var_sources are back. A
-- database that ran the old 1789793165 lost signing_keys and the two
-- pipeline columns; this re-creates them as the baseline declared them.
-- Every statement is a no-op on a database that never lost them.
CREATE TABLE IF NOT EXISTS signing_keys (
    kid text NOT NULL,
    kty text NOT NULL,
    jwk json NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT signing_keys_pkey PRIMARY KEY (kid)
);

ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS var_sources text,
                      ADD COLUMN IF NOT EXISTS nonce text;
