ALTER TABLE pipelines ADD COLUMN template BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE pipelines ADD COLUMN params_schema JSONB;
ALTER TABLE pipelines ADD COLUMN run_retention JSONB;
ALTER TABLE pipelines ADD COLUMN last_run_number INTEGER NOT NULL DEFAULT 0;
