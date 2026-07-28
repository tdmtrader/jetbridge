-- Snapshots are authorized by their owning team, never by a share/grant
-- relation. The old schema deduplicated manifests globally and delegated
-- authorization to agent_snapshot_grants. Before removing that relation,
-- reject every legacy row whose single owner cannot be determined exactly.
--
-- A retry for the same team is harmless (and is already constrained by the
-- legacy unique key). A row with no grant or distinct grants for multiple
-- teams is ambiguous; fail closed before making any DDL changes.
DO $$
DECLARE
    invalid_snapshot_id BIGINT;
    invalid_owner_count BIGINT;
BEGIN
    SELECT snapshot.id, count(DISTINCT grant_row.team_id)
    INTO invalid_snapshot_id, invalid_owner_count
    FROM agent_snapshots snapshot
    LEFT JOIN agent_snapshot_grants grant_row
      ON grant_row.snapshot_id = snapshot.id
    GROUP BY snapshot.id
    HAVING count(DISTINCT grant_row.team_id) <> 1
    ORDER BY snapshot.id
    LIMIT 1;

    IF invalid_snapshot_id IS NOT NULL THEN
        RAISE EXCEPTION
            'snapshot direct team ownership requires exactly one distinct grant team per snapshot'
            USING DETAIL = format(
                'snapshot %s has %s distinct grant teams',
                invalid_snapshot_id,
                invalid_owner_count
            );
    END IF;
END
$$;

ALTER TABLE agent_snapshots
    ADD COLUMN team_id INTEGER;

UPDATE agent_snapshots snapshot
SET team_id = owner.team_id
FROM (
    SELECT snapshot_id, min(team_id) AS team_id
    FROM agent_snapshot_grants
    GROUP BY snapshot_id
) owner
WHERE owner.snapshot_id = snapshot.id;

ALTER TABLE agent_snapshots
    ALTER COLUMN team_id SET NOT NULL,
    ADD CONSTRAINT agent_snapshots_team_id_fkey
        FOREIGN KEY (team_id) REFERENCES teams (id) ON DELETE RESTRICT;

-- A content digest remains globally deduplicated in agent_snapshot_locations,
-- but its manifest is a team-scoped authorization identity. Equal bytes can
-- therefore be independently owned without one team reading another's row.
ALTER TABLE agent_snapshots
    DROP CONSTRAINT agent_snapshots_type_name_type_version_digest_key,
    ADD CONSTRAINT agent_snapshots_team_type_version_digest_key
        UNIQUE (team_id, type_name, type_version, digest);

DROP INDEX agent_snapshots_type_state_created;
CREATE INDEX agent_snapshots_team_type_state_created
    ON agent_snapshots
        (team_id, type_name, type_version, content_state, created_at, id);

DROP TABLE agent_snapshot_grants;
