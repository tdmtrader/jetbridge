ALTER TABLE pipelines
  DROP CONSTRAINT pipelines_cache_scope_valid,
  DROP COLUMN cache_scope;
