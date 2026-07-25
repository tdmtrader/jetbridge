-- Downgrading loses exposure lineage. Full-tree exposure is re-derivable from
-- agent_snapshot_lineage, so nothing that the old schema can represent is lost;
-- static-selector path sets are not re-derivable and are dropped with the
-- tables. That is expected: the old schema has nowhere to keep them.
DROP TABLE agent_snapshot_exposure_paths;
DROP TABLE agent_snapshot_exposures;

DROP INDEX agent_snapshot_lineage_exposure_identity;
DROP INDEX agent_snapshots_exposure_tree_identity;
