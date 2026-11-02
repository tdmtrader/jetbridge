-- This migration once dropped signing_keys, pipelines.var_sources and
-- pipelines.nonce along with the idtoken credential manager and pipeline
-- var_sources. Both came back, so it no longer drops anything: a database
-- upgrading past it keeps them. A database that already ran the drop gets
-- them back, empty, from 1789793166.
SELECT 1;
