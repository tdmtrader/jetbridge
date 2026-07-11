CREATE TABLE agent_user_credentials (
    id               SERIAL PRIMARY KEY,
    user_id          INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    user_name        TEXT NOT NULL,
    kind             TEXT NOT NULL DEFAULT 'anthropic_oauth'
                     CHECK (kind IN ('anthropic_oauth', 'anthropic_api_key')),
    encrypted_token  TEXT NOT NULL,
    nonce            TEXT,
    expires_at       TIMESTAMPTZ,
    last_verified_at TIMESTAMPTZ,
    jira_account_id  TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_user_credentials_user_kind ON agent_user_credentials (user_id, kind);
