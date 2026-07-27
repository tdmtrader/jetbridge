-- Restore the static-selector half of exposure lineage exactly as 1773106127
-- created it. No rows are restored because none could exist: nothing ever
-- constructed a static selector, so the table was empty when it was dropped.

ALTER TABLE agent_snapshot_exposures
    DROP CONSTRAINT agent_snapshot_exposures_materialization_mode_check;

ALTER TABLE agent_snapshot_exposures
    ADD CONSTRAINT agent_snapshot_exposures_materialization_mode_check
    CHECK (materialization_mode IN ('full', 'static-selector'));

CREATE UNIQUE INDEX agent_snapshot_exposures_mode_identity
    ON agent_snapshot_exposures (id, materialization_mode);

CREATE TABLE agent_snapshot_exposure_paths (
    exposure_id          BIGINT NOT NULL,
    materialization_mode TEXT NOT NULL DEFAULT 'static-selector'
                           CHECK (materialization_mode = 'static-selector'),
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
