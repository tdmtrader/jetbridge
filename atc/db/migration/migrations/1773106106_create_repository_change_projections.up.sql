-- repository-change/v1 snapshots remain canonical. This table is a bounded,
-- disposable read model that can be rebuilt without contacting a Git remote.
CREATE TABLE agent_repository_change_projections (
    snapshot_id       BIGINT PRIMARY KEY REFERENCES agent_snapshots (id) ON DELETE CASCADE,
    status            TEXT NOT NULL CHECK (status IN ('pending', 'ready', 'unavailable', 'invalid')),
    projection_error  TEXT NOT NULL DEFAULT '',
    repository_id     TEXT,
    base_sha          TEXT,
    result_sha        TEXT,
    result_tree_sha   TEXT,
    representation    TEXT,
    files             JSONB,
    file_count        INTEGER,
    lines_added       INTEGER,
    lines_deleted     INTEGER,
    unified_diff      TEXT,
    truncated         BOOLEAN,
    truncation_reason TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (octet_length(projection_error) <= 4096),
    CHECK (
        (status = 'pending' AND projection_error = '') OR
        (status = 'ready' AND projection_error = '') OR
        (status IN ('unavailable', 'invalid') AND btrim(projection_error) <> '')
    ),
    CHECK (
        (status = 'ready'
            AND repository_id IS NOT NULL AND btrim(repository_id) <> ''
            AND base_sha IS NOT NULL AND btrim(base_sha) <> ''
            AND result_tree_sha IS NOT NULL AND btrim(result_tree_sha) <> ''
            AND representation IS NOT NULL AND representation IN ('git-tree', 'patch', 'bundle')
            AND files IS NOT NULL AND jsonb_typeof(files) = 'array'
            AND file_count IS NOT NULL AND file_count >= 0 AND jsonb_array_length(files) = file_count
            AND lines_added IS NOT NULL AND lines_added >= 0
            AND lines_deleted IS NOT NULL AND lines_deleted >= 0
            AND unified_diff IS NOT NULL AND octet_length(unified_diff) <= 65536
            AND truncated IS NOT NULL
            AND truncation_reason IS NOT NULL
            AND ((truncated AND btrim(truncation_reason) <> '') OR
                 (NOT truncated AND truncation_reason = '')))
        OR
        (status <> 'ready'
            AND repository_id IS NULL AND base_sha IS NULL AND result_sha IS NULL
            AND result_tree_sha IS NULL AND representation IS NULL AND files IS NULL
            AND file_count IS NULL AND lines_added IS NULL AND lines_deleted IS NULL
            AND unified_diff IS NULL AND truncated IS NULL AND truncation_reason IS NULL)
    )
);

CREATE INDEX agent_repository_change_projections_retryable
    ON agent_repository_change_projections (updated_at, snapshot_id)
    WHERE status IN ('pending', 'unavailable');
