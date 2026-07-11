CREATE TABLE agent_principals (
    id           SERIAL PRIMARY KEY,
    name         TEXT NOT NULL,                  -- e.g. 'harvest', 'gateway', 'ci-agent-review', 'agent-step'
    description  TEXT NOT NULL DEFAULT '',
    token_prefix TEXT NOT NULL,                  -- first 12 chars of the token, for display + O(1) lookup
    token_hash   TEXT NOT NULL,                  -- hex(sha256(full token)); raw token never stored
    scopes       TEXT[] NOT NULL DEFAULT '{}',   -- closed vocabulary, see 00-shared-contracts.md §4.1
    team_name    TEXT NOT NULL DEFAULT 'main',   -- join key to teams.name; '' = platform-global
    created_by   TEXT NOT NULL DEFAULT '',       -- concourse username that minted it
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ,                    -- NULL = no expiry
    revoked_at   TIMESTAMPTZ,                    -- NULL = active
    last_used_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX agent_principals_token_hash ON agent_principals (token_hash);
CREATE INDEX agent_principals_name ON agent_principals (name);

-- Dual-accept window attribution: rows written with the static
-- --agent-review-publish-token are attributed to this principal.
-- It has no token (empty hash can never match a 64-char sha256 hex).
INSERT INTO agent_principals (name, description, token_prefix, token_hash)
VALUES ('legacy-publish', 'static --agent-review-publish-token writes during the dual-accept window', '', '');
