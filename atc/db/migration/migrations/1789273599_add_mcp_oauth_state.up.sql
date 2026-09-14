CREATE TABLE mcp_oauth_state (
    id text PRIMARY KEY,
    kind text NOT NULL,
    data text NOT NULL,
    nonce text,
    expires_at timestamptz NOT NULL
);

CREATE INDEX mcp_oauth_state_kind ON mcp_oauth_state(kind);
CREATE INDEX mcp_oauth_state_expiry ON mcp_oauth_state(expires_at);
