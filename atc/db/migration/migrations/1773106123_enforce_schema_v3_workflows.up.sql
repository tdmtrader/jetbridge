UPDATE agent_workflow_definitions
SET live = false
WHERE live AND schema_version <> 3;

ALTER TABLE agent_workflow_definitions
    ADD CONSTRAINT agent_workflow_definitions_live_schema_v3_check
    CHECK (NOT live OR schema_version = 3);
