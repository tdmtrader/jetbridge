DROP INDEX agent_workflow_definitions_name_signature_version;
DROP INDEX agent_workflow_definitions_schema_version;

ALTER TABLE agent_workflow_definitions
    DROP CONSTRAINT agent_workflow_definitions_schema_signature_check,
    DROP CONSTRAINT agent_workflow_definitions_signature_version_check,
    DROP CONSTRAINT agent_workflow_definitions_schema_version_check,
    DROP COLUMN signature_version,
    DROP COLUMN schema_version;
