ALTER TABLE pipelines
  ADD COLUMN cache_scope text,
  ADD CONSTRAINT pipelines_cache_scope_valid CHECK (cache_scope IN ('template', 'none'));
