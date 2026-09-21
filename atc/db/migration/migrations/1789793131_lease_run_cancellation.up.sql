CREATE TABLE pipeline_run_cancellation_worker (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 owner_id text NOT NULL CHECK(length(owner_id) BETWEEN 1 AND 128),
 worker_epoch bigint NOT NULL CHECK(worker_epoch>0),
 renewed_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK(expires_at>renewed_at)
);
