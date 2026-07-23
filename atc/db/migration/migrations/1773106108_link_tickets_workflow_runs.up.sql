ALTER TABLE agent_tickets
    ADD COLUMN workflow_run_id BIGINT REFERENCES agent_workflow_runs (id) ON DELETE SET NULL,
    ADD COLUMN work_item_snapshot_id BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    ADD COLUMN repository_snapshot_id BIGINT REFERENCES agent_snapshots (id) ON DELETE RESTRICT,
    ADD COLUMN dispatch_reservation_key TEXT NOT NULL DEFAULT ''
        CHECK (dispatch_reservation_key = '' OR btrim(dispatch_reservation_key) <> '');

CREATE UNIQUE INDEX agent_tickets_workflow_run
    ON agent_tickets (workflow_run_id)
    WHERE workflow_run_id IS NOT NULL;

CREATE UNIQUE INDEX agent_tickets_dispatch_reservation
    ON agent_tickets (dispatch_reservation_key)
    WHERE dispatch_reservation_key <> '';

CREATE INDEX agent_tickets_work_item_snapshot
    ON agent_tickets (work_item_snapshot_id)
    WHERE work_item_snapshot_id IS NOT NULL;

CREATE INDEX agent_tickets_repository_snapshot
    ON agent_tickets (repository_snapshot_id)
    WHERE repository_snapshot_id IS NOT NULL;
