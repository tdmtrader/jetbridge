-- A resource config scope belongs to one team (ADR-0012). Until now a
-- config's shared scope (resource_id NULL) served every team whose resources
-- or resource types had that config, so one team's checks, puts, history
-- carries and clear-versions wrote into another team's version history, and
-- its metadata and last check showed there.
--
-- This gives every scope a team and every team that used a shared scope its
-- own copy of that scope's history:
--
--   * a unique-history scope takes its resource's team, and a deprecated
--     scope the team of the resource it was deprecated from (or of a
--     resource still pointing at it); one whose resource is gone keeps no
--     team, and nothing can find it again;
--   * a live shared scope's teams are those of the resources that point at
--     it, active or not, and of the resource types on its config. The lowest
--     team id keeps the scope; every other team gets a new scope with the
--     same versions, check_order and metadata, and its resources point at it;
--   * a live shared scope no resource or resource type uses (a task's image
--     check alone) is deleted: the next such check creates its team's scope;
--   * the last check's times are copied, but a last check build stays only
--     on a scope of that build's team (a single team's scope whose last
--     check build cannot be placed keeps it);
--   * a resource left pointing at a scope of another team (only a deprecated
--     scope can be one) is detached, and its next check attaches it.
--
-- Build inputs and outputs, next build inputs, pins and disabled versions name
-- a resource and a version digest, never a scope or a version row, so they
-- resolve in each team's copy as they did in the shared scope.
--
-- A trigger then refuses any resource pointing at a scope of another team.

-- Take every table this locks beyond a row before anything else, so no
-- statement below waits for a lock an outgoing web holds while that web
-- waits for a table this holds. Its transactions take them in any order: a
-- put or a move locks its resource row and then reads scopes; the
-- deprecated-scope and config collectors delete scopes, whose ON DELETE SET
-- NULL then writes resources; and destroy-team deletes a team row and then
-- its pipelines' resources, while the new team_id foreign key needs SHARE
-- ROW EXCLUSIVE on teams. So the three are taken together or not at all: an
-- attempt that times out or is chosen as a deadlock's victim rolls back to
-- its savepoint, releasing what it took, and tries again, giving up after 60
-- attempts. EXCLUSIVE on resources still lets an outgoing web read them, but
-- blocks every write that references a resource row; ACCESS EXCLUSIVE on
-- scopes blocks every read of them; SHARE ROW EXCLUSIVE on teams blocks
-- writes to teams. All three hold until this commits.
DO $$
DECLARE
    attempt integer := 0;
BEGIN
    LOOP
        attempt := attempt + 1;
        BEGIN
            SET LOCAL lock_timeout = '5s';
            LOCK TABLE resources IN EXCLUSIVE MODE;
            LOCK TABLE resource_config_scopes IN ACCESS EXCLUSIVE MODE;
            LOCK TABLE teams IN SHARE ROW EXCLUSIVE MODE;
            SET LOCAL lock_timeout = 0;
            EXIT;
        EXCEPTION WHEN lock_not_available OR deadlock_detected THEN
            IF attempt >= 60 THEN
                RAISE;
            END IF;
            PERFORM pg_sleep(0.1 * attempt);
        END;
    END LOOP;
END;
$$;

ALTER TABLE resource_config_scopes
    ADD COLUMN team_id integer REFERENCES teams(id) ON DELETE CASCADE;

UPDATE resource_config_scopes s
SET team_id = p.team_id
FROM resources r
JOIN pipelines p ON p.id = r.pipeline_id
WHERE s.resource_id IS NOT NULL
  AND r.id = s.resource_id;

UPDATE resource_config_scopes s
SET team_id = p.team_id
FROM resources r
JOIN pipelines p ON p.id = r.pipeline_id
WHERE s.resource_id IS NULL
  AND s.deprecated_at IS NOT NULL
  AND r.id = s.deprecated_from_resource_id;

UPDATE resource_config_scopes s
SET team_id = pointer.team_id
FROM (
    SELECT DISTINCT ON (r.resource_config_scope_id) r.resource_config_scope_id AS scope_id, p.team_id
    FROM resources r
    JOIN pipelines p ON p.id = r.pipeline_id
    WHERE r.resource_config_scope_id IS NOT NULL
    ORDER BY r.resource_config_scope_id, p.team_id
) pointer
WHERE s.id = pointer.scope_id
  AND s.deprecated_at IS NOT NULL
  AND s.team_id IS NULL;

CREATE TEMP TABLE scope_teams ON COMMIT DROP AS
SELECT s.id AS scope_id, p.team_id
FROM resource_config_scopes s
JOIN resources r ON r.resource_config_scope_id = s.id
JOIN pipelines p ON p.id = r.pipeline_id
WHERE s.resource_id IS NULL AND s.deprecated_at IS NULL
UNION
SELECT s.id, p.team_id
FROM resource_config_scopes s
JOIN resource_types rt ON rt.resource_config_id = s.resource_config_id
JOIN pipelines p ON p.id = rt.pipeline_id
WHERE s.resource_id IS NULL AND s.deprecated_at IS NULL;

CREATE TEMP TABLE scope_keepers ON COMMIT DROP AS
SELECT scope_id, min(team_id) AS team_id
FROM scope_teams
GROUP BY scope_id;

UPDATE resource_config_scopes s
SET team_id = k.team_id
FROM scope_keepers k
WHERE s.id = k.scope_id;

CREATE TEMP TABLE scope_copies ON COMMIT DROP AS
SELECT st.scope_id AS from_id,
       st.team_id,
       nextval('resource_config_scopes_id_seq') AS to_id
FROM scope_teams st
JOIN scope_keepers k USING (scope_id)
WHERE st.team_id <> k.team_id;

-- The team of a scope's last check build: a build's own, or for an in-memory
-- check build, which has no builds row, the team of the resource running it.
CREATE TEMP TABLE check_build_teams ON COMMIT DROP AS
SELECT b.id AS build_id, b.team_id
FROM builds b
WHERE b.id IN (SELECT last_check_build_id FROM resource_config_scopes)
UNION
SELECT r.in_memory_build_id, p.team_id
FROM resources r
JOIN pipelines p ON p.id = r.pipeline_id
WHERE r.in_memory_build_id IN (SELECT last_check_build_id FROM resource_config_scopes)
  AND NOT EXISTS (SELECT 1 FROM builds b WHERE b.id = r.in_memory_build_id);

DROP INDEX resource_config_scopes_resource_config_id_uniq;

INSERT INTO resource_config_scopes
    (id, resource_config_id, resource_id, team_id,
     last_check_start_time, last_check_end_time, last_check_succeeded,
     last_check_build_id, last_check_build_plan)
SELECT c.to_id, s.resource_config_id, NULL, c.team_id,
       s.last_check_start_time, s.last_check_end_time, s.last_check_succeeded,
       b.build_id, CASE WHEN b.build_id IS NULL THEN '{}'::json ELSE s.last_check_build_plan END
FROM scope_copies c
JOIN resource_config_scopes s ON s.id = c.from_id
LEFT JOIN check_build_teams b ON b.build_id = s.last_check_build_id AND b.team_id = c.team_id;

INSERT INTO resource_config_versions
    (resource_config_scope_id, version, version_md5, version_sha256,
     metadata, check_order, span_context)
SELECT c.to_id, v.version, v.version_md5, v.version_sha256,
       v.metadata, v.check_order, v.span_context
FROM scope_copies c
JOIN resource_config_versions v ON v.resource_config_scope_id = c.from_id;

UPDATE resources r
SET resource_config_scope_id = c.to_id
FROM scope_copies c, pipelines p
WHERE r.resource_config_scope_id = c.from_id
  AND p.id = r.pipeline_id
  AND p.team_id = c.team_id;

UPDATE resource_config_scopes s
SET last_check_build_id = NULL, last_check_build_plan = '{}'::json
WHERE s.id IN (SELECT scope_id FROM scope_keepers)
  AND s.last_check_build_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM check_build_teams b
      WHERE b.build_id = s.last_check_build_id AND b.team_id = s.team_id
  )
  AND (
      EXISTS (SELECT 1 FROM check_build_teams b WHERE b.build_id = s.last_check_build_id)
      OR s.id IN (SELECT from_id FROM scope_copies)
  );

DELETE FROM resource_config_scopes
WHERE deprecated_at IS NULL AND team_id IS NULL;

UPDATE resources r
SET resource_config_scope_id = NULL
FROM resource_config_scopes s, pipelines p
WHERE s.id = r.resource_config_scope_id
  AND p.id = r.pipeline_id
  AND s.team_id IS DISTINCT FROM p.team_id;

CREATE UNIQUE INDEX resource_config_scopes_config_team_uniq
    ON resource_config_scopes (resource_config_id, team_id)
    WHERE resource_id IS NULL AND deprecated_at IS NULL;

ALTER TABLE resource_config_scopes
    ADD CONSTRAINT resource_config_scopes_live_scope_has_team
    CHECK (deprecated_at IS NOT NULL OR team_id IS NOT NULL);

CREATE FUNCTION resource_scope_is_own_teams() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.resource_config_scope_id IS NOT NULL AND EXISTS (
        SELECT 1
        FROM resource_config_scopes s, pipelines p
        WHERE s.id = NEW.resource_config_scope_id
          AND p.id = NEW.pipeline_id
          AND s.team_id IS DISTINCT FROM p.team_id
    ) THEN
        RAISE EXCEPTION 'resource % cannot use resource config scope % of another team',
            NEW.id, NEW.resource_config_scope_id
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER resource_scope_is_own_teams
    BEFORE INSERT OR UPDATE OF resource_config_scope_id, pipeline_id ON resources
    FOR EACH ROW EXECUTE FUNCTION resource_scope_is_own_teams();
