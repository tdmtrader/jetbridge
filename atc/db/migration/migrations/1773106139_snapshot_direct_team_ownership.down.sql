-- The legacy global manifest identity cannot represent independently owned
-- equal digests, so do not silently coalesce them during a rollback.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM agent_snapshots
        GROUP BY type_name, type_version, digest
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION
            'snapshot direct team ownership cannot downgrade team-scoped duplicate content identities';
    END IF;
END
$$;

CREATE TABLE agent_snapshot_grants (
    id          BIGSERIAL PRIMARY KEY,
    snapshot_id BIGINT NOT NULL REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    team_id     INTEGER NOT NULL CHECK (team_id > 0),
    granted_by  TEXT NOT NULL CHECK (btrim(granted_by) <> ''),
    reason      TEXT NOT NULL CHECK (btrim(reason) <> ''),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (snapshot_id, team_id)
);

INSERT INTO agent_snapshot_grants
    (snapshot_id, team_id, granted_by, reason, created_at)
SELECT
    id,
    team_id,
    'migration-1773106139',
    'direct snapshot owner',
    created_at
FROM agent_snapshots;

CREATE INDEX agent_snapshot_grants_team_snapshot
    ON agent_snapshot_grants (team_id, snapshot_id);

ALTER TABLE agent_snapshots
    DROP CONSTRAINT agent_snapshots_team_type_version_digest_key,
    ADD CONSTRAINT agent_snapshots_type_name_type_version_digest_key
        UNIQUE (type_name, type_version, digest);

DROP INDEX agent_snapshots_team_type_state_created;
CREATE INDEX agent_snapshots_type_state_created
    ON agent_snapshots (type_name, type_version, content_state, created_at, id);

ALTER TABLE agent_snapshots
    DROP CONSTRAINT agent_snapshots_team_id_fkey,
    DROP COLUMN team_id;
