DROP INDEX agent_tickets_repository_snapshot;
DROP INDEX agent_tickets_work_item_snapshot;
DROP INDEX agent_tickets_dispatch_reservation;
DROP INDEX agent_tickets_workflow_run;

ALTER TABLE agent_tickets
    DROP COLUMN dispatch_reservation_key,
    DROP COLUMN repository_snapshot_id,
    DROP COLUMN work_item_snapshot_id,
    DROP COLUMN workflow_run_id;
