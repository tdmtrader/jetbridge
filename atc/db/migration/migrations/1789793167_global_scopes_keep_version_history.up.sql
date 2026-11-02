-- Global resources became unconditional in 0.4.0: a resource's versions live
-- in its config's one global scope (resource_id NULL) unless its base type
-- asks for a unique history. A deployment that ran with global resources off
-- kept every resource's versions in a scope of its own (resource_id set), and
-- the web, finding no global scope, started an empty one: each resource's
-- version history appeared lost. This folds every live per-resource scope
-- whose base type does not ask for a unique history into its config's global
-- scope, keeping its versions:
--
--   * a config with no global scope promotes its most recently checked
--     per-resource scope to be the global one;
--   * a config that already has one (a deployment that ran 0.4.0 or 0.4.1)
--     keeps it;
--   * every other per-resource scope of the config adds the versions the
--     global scope lacks, its resources move to the global scope, and it
--     goes.
--
-- check_order is numbered per scope, so a long-lived scope's old versions can
-- carry higher numbers than the kept scope's newest. The kept scope's versions
-- are therefore moved above the highest number being merged in: its newest
-- version stays the latest, and the merged history sits below it.
--
-- Build inputs and outputs, pins and disabled versions name a resource and a
-- version, not a scope, so they carry over unchanged. Where two resources
-- shared a config, the versions merged in below the kept scope's keep their
-- order relative to their own scope's history. A database that ran with global resources on has no such scope
-- and is untouched.

CREATE TEMP TABLE unique_history_configs ON COMMIT DROP AS
WITH RECURSIVE chain(config_id, current_config, depth) AS (
    SELECT id, id, 0 FROM resource_configs
  UNION ALL
    SELECT chain.config_id, cache.resource_config_id, chain.depth + 1
    FROM chain
    JOIN resource_configs rc ON rc.id = chain.current_config
    JOIN resource_caches cache ON cache.id = rc.resource_cache_id
    WHERE cache.resource_config_id IS NOT NULL
      AND chain.depth < 64
)
SELECT DISTINCT chain.config_id
FROM chain
JOIN resource_configs rc ON rc.id = chain.current_config
JOIN base_resource_types brt ON brt.id = rc.base_resource_type_id
WHERE brt.unique_version_history;

CREATE TEMP TABLE scope_folds ON COMMIT DROP AS
SELECT s.id AS scope_id, s.resource_config_id, s.last_check_end_time
FROM resource_config_scopes s
WHERE s.resource_id IS NOT NULL
  AND s.deprecated_at IS NULL
  AND s.resource_config_id NOT IN (SELECT config_id FROM unique_history_configs);

CREATE TEMP TABLE scope_targets ON COMMIT DROP AS
SELECT DISTINCT ON (f.resource_config_id)
       f.resource_config_id,
       COALESCE(g.id, f.scope_id) AS target_id,
       g.id IS NOT NULL AS target_existed
FROM scope_folds f
LEFT JOIN resource_config_scopes g
       ON g.resource_config_id = f.resource_config_id
      AND g.resource_id IS NULL
      AND g.deprecated_at IS NULL
ORDER BY f.resource_config_id, f.last_check_end_time DESC, f.scope_id;

-- Move the kept scope's versions above every version merged into it, so its
-- newest stays the latest.
UPDATE resource_config_versions v
SET check_order = v.check_order + history.max_order
FROM scope_targets t
JOIN (
    SELECT f.resource_config_id, max(fv.check_order) AS max_order
    FROM scope_folds f
    JOIN scope_targets ft ON ft.resource_config_id = f.resource_config_id
    JOIN resource_config_versions fv ON fv.resource_config_scope_id = f.scope_id
    WHERE f.scope_id <> ft.target_id
    GROUP BY f.resource_config_id
) history ON history.resource_config_id = t.resource_config_id
WHERE v.resource_config_scope_id = t.target_id;

UPDATE resource_config_scopes s
SET resource_id = NULL
FROM scope_targets t
WHERE s.id = t.target_id
  AND NOT t.target_existed;

INSERT INTO resource_config_versions
    (version, version_md5, metadata, check_order, resource_config_scope_id, span_context, version_sha256)
SELECT DISTINCT ON (t.target_id, v.version_sha256)
       v.version, v.version_md5, v.metadata, v.check_order, t.target_id, v.span_context, v.version_sha256
FROM scope_folds f
JOIN scope_targets t ON t.resource_config_id = f.resource_config_id
JOIN resource_config_versions v ON v.resource_config_scope_id = f.scope_id
WHERE f.scope_id <> t.target_id
ORDER BY t.target_id, v.version_sha256, v.check_order DESC
ON CONFLICT (resource_config_scope_id, version_sha256) DO NOTHING;

UPDATE resources r
SET resource_config_scope_id = t.target_id
FROM scope_folds f
JOIN scope_targets t ON t.resource_config_id = f.resource_config_id
WHERE r.resource_config_scope_id = f.scope_id
  AND f.scope_id <> t.target_id;

DELETE FROM resource_config_scopes s
USING scope_folds f
JOIN scope_targets t ON t.resource_config_id = f.resource_config_id
WHERE s.id = f.scope_id
  AND f.scope_id <> t.target_id;
