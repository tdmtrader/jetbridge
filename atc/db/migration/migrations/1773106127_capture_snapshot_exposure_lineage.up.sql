-- Exposure lineage answers "what was this step actually shown?" — the question
-- behind "did the judge really look at the diff?". It is production-occurrence
-- data: it belongs to one invocation, not to a value, so it is recorded here
-- and never inside the sealed bytes. It is deliberately not stored on
-- agent_snapshots either, whose (type_name, type_version, digest) row is the
-- deduplicated content identity and must stay a pure function of those bytes.

-- These two identity indexes are what let an exposure row be checked against
-- the mount it claims to describe, instead of being trusted.
CREATE UNIQUE INDEX agent_snapshots_exposure_tree_identity
    ON agent_snapshots (id, digest);

CREATE UNIQUE INDEX agent_snapshot_lineage_exposure_identity
    ON agent_snapshot_lineage (production_id, input_port, input_snapshot_id);

CREATE TABLE agent_snapshot_exposures (
    id                   BIGSERIAL PRIMARY KEY,
    production_id        BIGINT NOT NULL,
    input_port           TEXT NOT NULL
                           CHECK (btrim(input_port) = input_port AND input_port <> ''),
    input_snapshot_id    BIGINT NOT NULL,
    -- 'dynamic' is absent on purpose. Agent-driven partial mounting cannot have
    -- its path set known at admission, so no honest lineage row exists for it;
    -- it must be unrepresentable rather than merely discouraged.
    materialization_mode TEXT NOT NULL
                           CHECK (materialization_mode IN ('full', 'static-selector')),
    -- The exposed tree. The composite foreign key below binds it to the digest
    -- of the snapshot actually bound to this port, so a producer cannot claim
    -- to have been shown a tree it was never given.
    tree_digest          TEXT NOT NULL CHECK (tree_digest ~ '^sha256:[0-9a-f]{64}$'),
    -- Where the tree was materialized in the step's filesystem, when it was
    -- materialized at all. NULL means the input was exposed only by reference.
    mount_path           TEXT
                           CHECK (mount_path IS NULL
                                  OR (btrim(mount_path) = mount_path
                                      AND mount_path <> ''
                                      AND mount_path <> '/'
                                      AND mount_path NOT LIKE '%/'
                                      AND mount_path NOT LIKE '%//%'
                                      AND mount_path !~ '(^|/)\.\.?(/|$)')),
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (production_id, input_port),
    FOREIGN KEY (production_id, input_port, input_snapshot_id)
        REFERENCES agent_snapshot_lineage (production_id, input_port, input_snapshot_id)
        ON DELETE CASCADE,
    FOREIGN KEY (input_snapshot_id, tree_digest)
        REFERENCES agent_snapshots (id, digest)
        ON DELETE RESTRICT
);

CREATE INDEX agent_snapshot_exposures_production
    ON agent_snapshot_exposures (production_id, input_port);

-- The parent half of the constraint that makes an enumerated path set legal
-- only under a static selector.
CREATE UNIQUE INDEX agent_snapshot_exposures_mode_identity
    ON agent_snapshot_exposures (id, materialization_mode);

CREATE TABLE agent_snapshot_exposure_paths (
    exposure_id          BIGINT NOT NULL,
    -- Denormalized so the composite foreign key can enforce, structurally,
    -- that only a static selector enumerates paths. A full exposure records
    -- the whole tree, and a partial path set under it would be a lie.
    materialization_mode TEXT NOT NULL DEFAULT 'static-selector'
                           CHECK (materialization_mode = 'static-selector'),
    -- Archive-relative, literal, and enumerable: the only namespace verifiable
    -- at seal time, and the reason a partial exposure can be recorded at all.
    -- A pattern is what a dynamic selector would need, so patterns are refused.
    path                 TEXT NOT NULL
                           CHECK (btrim(path) = path
                                  AND path <> ''
                                  AND path NOT LIKE '/%'
                                  AND path NOT LIKE '%/'
                                  AND path NOT LIKE '%//%'
                                  AND path !~ '(^|/)\.\.?(/|$)'
                                  AND path !~ '[*?]'),
    digest               TEXT NOT NULL CHECK (digest ~ '^sha256:[0-9a-f]{64}$'),
    PRIMARY KEY (exposure_id, path),
    FOREIGN KEY (exposure_id, materialization_mode)
        REFERENCES agent_snapshot_exposures (id, materialization_mode)
        ON UPDATE CASCADE ON DELETE CASCADE
);

-- Backfill. Every input already recorded in lineage was materialized whole —
-- partial mounting has never existed — so 'full' is the accurate historical
-- claim, and the exposed tree is the input snapshot's own digest. The mount
-- path was not captured at the time and is left unknown rather than invented.
INSERT INTO agent_snapshot_exposures
    (production_id, input_port, input_snapshot_id, materialization_mode, tree_digest, created_at)
SELECT lineage.production_id, lineage.input_port, lineage.input_snapshot_id,
       'full', input.digest, production.created_at
FROM agent_snapshot_lineage lineage
JOIN agent_snapshots input ON input.id = lineage.input_snapshot_id
JOIN agent_snapshot_productions production ON production.id = lineage.production_id;
