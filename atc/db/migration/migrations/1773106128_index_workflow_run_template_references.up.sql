-- Reclaiming abandoned server-owned workflow-run templates asks, for every
-- candidate template, whether any durable workflow run still cites it.
-- agent_workflow_runs.template_pipeline_id deliberately carries no foreign key
-- (1773106103 dropped it: the durable run keeps its execution provenance as
-- immutable scalars that outlive the disposable pipeline rows), so the column
-- has never been indexed. The resource-capture output read and finalize paths
-- run the same probe.
CREATE INDEX agent_workflow_runs_template_pipeline
    ON agent_workflow_runs (template_pipeline_id)
    WHERE template_pipeline_id IS NOT NULL;
